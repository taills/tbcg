package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"example.com/tbcg/server/internal/economy"
)

func TestEconomyClosedDatabaseReturnsExplicitErrors(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("close isolated test store: %v", err)
	}
	service := economy.New(store.db)
	command := economy.Command{
		GameID: "game", RealmID: "realm", ActorSubject: "actor", Operation: "reward",
		IdempotencyKey: "key", CharacterID: "character", CurrencyCode: "gold",
		Amount: 1, OwnerEpoch: 1, EventType: "reward",
	}
	if _, err := service.Settle(ctx, command); err == nil {
		t.Fatal("settlement succeeded after closing database")
	}
	if _, err := service.Recover(ctx, command); err == nil {
		t.Fatal("recovery succeeded after closing database")
	}
	if _, err := service.ClaimOutbox(ctx, "consumer", time.Second); err == nil {
		t.Fatal("outbox claim succeeded after closing database")
	}
	if _, err := service.ProcessOutbox(ctx, "consumer", "event", "consumer|token", func(context.Context, *sql.Tx, economy.OutboxMessage) error { return nil }); err == nil {
		t.Fatal("outbox processing succeeded after closing database")
	}
}

func TestEconomyValidationAndRecoveryBoundaries(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	service := economy.New(store.db)
	valid := fixture.command("boundary-operation", 2)
	for _, invalid := range []economy.Command{
		{},
		{GameID: valid.GameID, RealmID: valid.RealmID},
		fixture.command("zero-amount", 0),
		fixture.command("negative-amount", -1),
	} {
		if _, err := service.Settle(ctx, invalid); !errors.Is(err, economy.ErrInvalidCommand) {
			t.Errorf("invalid settlement command error = %v, want ErrInvalidCommand", err)
		}
	}
	if _, err := service.Recover(ctx, valid); !errors.Is(err, economy.ErrOperationNotFound) {
		t.Fatalf("recover absent operation error = %v, want ErrOperationNotFound", err)
	}
	if _, err := service.Recover(ctx, economy.Command{}); !errors.Is(err, economy.ErrInvalidCommand) {
		t.Fatalf("recover invalid command error = %v, want ErrInvalidCommand", err)
	}
	if message, err := service.ClaimOutbox(ctx, "consumer", time.Second); err != nil || message != nil {
		t.Fatalf("empty outbox claim = %+v, error %v; want no message", message, err)
	}
	if _, err := service.ClaimOutbox(ctx, " ", time.Second); err == nil {
		t.Fatal("outbox claim accepted an empty consumer")
	}
	if _, err := service.ClaimOutbox(ctx, "consumer", 0); err == nil {
		t.Fatal("outbox claim accepted a non-positive lease")
	}
	if processed, err := service.ProcessOutbox(ctx, "", "event", "token", nil); err == nil || processed {
		t.Fatalf("outbox processor accepted an empty consumer: processed=%v error=%v", processed, err)
	}
	if processed, err := service.ProcessOutbox(ctx, "consumer", "event", "other|token", nil); err == nil || processed {
		t.Fatalf("outbox processor accepted another consumer's lease: processed=%v error=%v", processed, err)
	}
	if processed, err := service.ProcessOutbox(ctx, "consumer", "event", "consumer|token", nil); err == nil || processed {
		t.Fatalf("outbox processor accepted a missing handler: processed=%v error=%v", processed, err)
	}
}

func TestEconomyRewardSettlementIsIdempotentAndAtomic(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	service := economy.New(store.db)
	command := fixture.command("reward-1", 25)

	first, err := service.Settle(ctx, command)
	if err != nil {
		t.Fatalf("settle reward: %v", err)
	}
	if first.Balance != 25 {
		t.Fatalf("first balance = %d, want 25", first.Balance)
	}

	duplicate, err := service.Settle(ctx, command)
	if err != nil {
		t.Fatalf("retry committed reward: %v", err)
	}
	if duplicate != first {
		t.Fatalf("retry result = %+v, want original %+v", duplicate, first)
	}

	changedPayload := fixture.command("reward-1", 26)
	if _, err := service.Settle(ctx, changedPayload); !errors.Is(err, economy.ErrIdempotencyConflict) {
		t.Fatalf("same key with changed payload error = %v, want ErrIdempotencyConflict", err)
	}
	if _, err := service.Recover(ctx, changedPayload); !errors.Is(err, economy.ErrIdempotencyConflict) {
		t.Fatalf("recover same key with changed payload error = %v, want ErrIdempotencyConflict", err)
	}

	changedEvent := command
	changedEvent.EventType = "economy.quest.rewarded"
	if _, err := service.Settle(ctx, changedEvent); !errors.Is(err, economy.ErrIdempotencyConflict) {
		t.Fatalf("same key with changed event type error = %v, want ErrIdempotencyConflict", err)
	}
	assertEconomyState(t, ctx, store, fixture, 25, 1, 1)
}

func TestEconomyWalletWriteFailureRollsBackReservation(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	if _, err := store.db.ExecContext(ctx, `
CREATE FUNCTION reject_test_wallet_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected wallet update failure'; END $$;
CREATE TRIGGER reject_test_wallet BEFORE UPDATE ON wallets
FOR EACH ROW EXECUTE FUNCTION reject_test_wallet_update()`); err != nil {
		t.Fatalf("install wallet update failure injection: %v", err)
	}
	t.Cleanup(func() {
		_, err := store.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS reject_test_wallet ON wallets; DROP FUNCTION IF EXISTS reject_test_wallet_update()`)
		if err != nil {
			t.Errorf("remove wallet update failure injection: %v", err)
		}
	})
	if _, err := economy.New(store.db).Settle(ctx, fixture.command("wallet-update-fail", 5)); err == nil {
		t.Fatal("settlement succeeded after injected wallet write failure")
	}
	assertEconomyState(t, ctx, store, fixture, 0, 0, 0)
}

func TestEconomyLedgerFailureRollsBackAndDoesNotConfirm(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	if _, err := store.db.ExecContext(ctx, `
CREATE FUNCTION reject_test_ledger_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected ledger failure'; END $$;
CREATE TRIGGER reject_test_ledger BEFORE INSERT ON wallet_ledger
FOR EACH ROW EXECUTE FUNCTION reject_test_ledger_insert()`); err != nil {
		t.Fatalf("install ledger failure injection: %v", err)
	}
	t.Cleanup(func() {
		_, err := store.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS reject_test_ledger ON wallet_ledger; DROP FUNCTION IF EXISTS reject_test_ledger_insert()`)
		if err != nil {
			t.Errorf("remove ledger failure injection: %v", err)
		}
	})

	if result, err := economy.New(store.db).Settle(ctx, fixture.command("reward-fail", 25)); err == nil || result != (economy.Result{}) {
		t.Fatalf("failed settlement returned result %+v, error %v; want no confirmation", result, err)
	}
	assertEconomyState(t, ctx, store, fixture, 0, 0, 0)
}

func TestEconomyCommittedResultCanBeRecoveredAfterLostResponse(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	service := economy.New(store.db)
	command := fixture.command("reward-lost-response", 9)

	if _, err := service.Settle(ctx, command); err != nil {
		t.Fatalf("commit reward before simulated process crash: %v", err)
	}
	// 首个进程丢弃响应后退出；新进程只凭数据库中的唯一键恢复结果。
	var databaseName string
	if err := store.db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read isolated database name for recovery process: %v", err)
	}
	dsn, err := databaseURLForTestDatabase(os.Getenv("TBCG_TEST_DATABASE_URL"), databaseName)
	if err != nil {
		t.Fatalf("build isolated database URL for recovery process: %v", err)
	}
	commandEnv := []struct{ key, value string }{
		{"TBCG_RECOVERY_HELPER", "1"}, {"TBCG_RECOVERY_DSN", dsn},
		{"TBCG_RECOVERY_GAME", command.GameID}, {"TBCG_RECOVERY_REALM", command.RealmID},
		{"TBCG_RECOVERY_ACTOR", command.ActorSubject}, {"TBCG_RECOVERY_OPERATION", command.Operation},
		{"TBCG_RECOVERY_KEY", command.IdempotencyKey}, {"TBCG_RECOVERY_CHARACTER", command.CharacterID},
		{"TBCG_RECOVERY_CURRENCY", command.CurrencyCode}, {"TBCG_RECOVERY_AMOUNT", "9"},
		{"TBCG_RECOVERY_EVENT", command.EventType}, {"TBCG_RECOVERY_EPOCH", "7"},
	}
	env := os.Environ()
	for _, variable := range commandEnv {
		env = setEnvironmentValue(env, variable.key, variable.value)
	}
	process := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEconomyRecoveryInFreshProcessHelper$")
	process.Env = env
	output, err := process.CombinedOutput()
	if err != nil {
		t.Fatalf("recover committed result in a fresh process: %v\\n%s", err, output)
	}
	assertEconomyState(t, ctx, store, fixture, 9, 1, 1)
}

func TestEconomyRecoveryInFreshProcessHelper(t *testing.T) {
	if os.Getenv("TBCG_RECOVERY_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config, err := pgx.ParseConfig(os.Getenv("TBCG_RECOVERY_DSN"))
	if err != nil {
		t.Fatalf("parse recovery test database URL: %v", err)
	}
	db := stdlib.OpenDB(*config)
	defer db.Close()
	amount, err := strconv.ParseInt(os.Getenv("TBCG_RECOVERY_AMOUNT"), 10, 64)
	if err != nil {
		t.Fatalf("parse recovery amount: %v", err)
	}
	epoch, err := strconv.ParseInt(os.Getenv("TBCG_RECOVERY_EPOCH"), 10, 64)
	if err != nil {
		t.Fatalf("parse recovery owner epoch: %v", err)
	}
	command := economy.Command{
		GameID: os.Getenv("TBCG_RECOVERY_GAME"), RealmID: os.Getenv("TBCG_RECOVERY_REALM"),
		ActorSubject: os.Getenv("TBCG_RECOVERY_ACTOR"), Operation: os.Getenv("TBCG_RECOVERY_OPERATION"),
		IdempotencyKey: os.Getenv("TBCG_RECOVERY_KEY"), CharacterID: os.Getenv("TBCG_RECOVERY_CHARACTER"),
		CurrencyCode: os.Getenv("TBCG_RECOVERY_CURRENCY"), Amount: amount,
		OwnerEpoch: epoch, EventType: os.Getenv("TBCG_RECOVERY_EVENT"),
	}
	result, err := economy.New(db).Recover(ctx, command)
	if err != nil {
		t.Fatalf("load committed result in fresh process: %v", err)
	}
	if result != (economy.Result{Balance: amount, Amount: amount}) {
		t.Fatalf("fresh-process recovery returned %+v", result)
	}
}

func TestEconomyOwnerTakeoverWaitsForExistingSettlement(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	const advisoryKey = int64(71826008)
	if _, err := store.db.ExecContext(ctx, `
CREATE FUNCTION wait_for_wallet_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM pg_advisory_xact_lock(71826008); RETURN NEW; END $$;
CREATE TRIGGER wait_for_wallet_write_trigger BEFORE UPDATE OF balance ON wallets
FOR EACH ROW EXECUTE FUNCTION wait_for_wallet_write()`); err != nil {
		t.Fatalf("install settlement barrier: %v", err)
	}
	t.Cleanup(func() {
		_, err := store.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS wait_for_wallet_write_trigger ON wallets; DROP FUNCTION IF EXISTS wait_for_wallet_write()`)
		if err != nil {
			t.Errorf("remove settlement barrier: %v", err)
		}
	})
	locker, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve settlement barrier connection: %v", err)
	}
	defer locker.Close()
	if _, err := locker.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryKey); err != nil {
		t.Fatalf("hold settlement barrier: %v", err)
	}
	defer locker.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryKey)

	settlementDone := make(chan error, 1)
	go func() {
		_, err := economy.New(store.db).Settle(ctx, fixture.command("before-takeover", 1))
		settlementDone <- err
	}()
	awaitLockWaitingBackend(t, ctx, store)
	takeoverDone := make(chan error, 1)
	go func() {
		_, err := store.db.ExecContext(ctx, `UPDATE owner_guards SET owner_epoch=owner_epoch+1 WHERE game_id=$1 AND realm_id=$2`, fixture.gameID, fixture.realmID)
		takeoverDone <- err
	}()
	select {
	case err := <-takeoverDone:
		t.Fatalf("owner takeover crossed an active settlement guard: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := locker.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, advisoryKey); err != nil {
		t.Fatalf("release settlement barrier: %v", err)
	}
	select {
	case err := <-settlementDone:
		if err != nil {
			t.Fatalf("commit operation fenced before takeover: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("settlement did not finish after releasing its barrier")
	}
	select {
	case err := <-takeoverDone:
		if err != nil {
			t.Fatalf("advance owner epoch after settlement: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner takeover remained blocked after settlement commit")
	}
	stale := fixture.command("after-takeover", 1)
	if _, err := economy.New(store.db).Settle(ctx, stale); !errors.Is(err, economy.ErrOwnerEpochMismatch) {
		t.Fatalf("stale owner settlement error = %v, want ErrOwnerEpochMismatch", err)
	}
	assertEconomyState(t, ctx, store, fixture, 1, 1, 1)
}

func TestEconomyMissingWalletRollsBackIdempotencyReservation(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	if _, err := store.db.ExecContext(ctx, `DELETE FROM wallets WHERE game_id=$1 AND realm_id=$2 AND character_id=$3`, fixture.gameID, fixture.realmID, fixture.characterID); err != nil {
		t.Fatalf("remove fixture wallet: %v", err)
	}
	command := fixture.command("missing-wallet", 6)
	if _, err := economy.New(store.db).Settle(ctx, command); err == nil {
		t.Fatal("settlement succeeded without a wallet")
	}
	var operationCount, ledgerCount, outboxCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM operation_results WHERE game_id=$1 AND realm_id=$2`, fixture.gameID, fixture.realmID).Scan(&operationCount); err != nil {
		t.Fatalf("count operation reservations after missing wallet: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM wallet_ledger WHERE game_id=$1 AND realm_id=$2`, fixture.gameID, fixture.realmID).Scan(&ledgerCount); err != nil {
		t.Fatalf("count ledger rows after missing wallet: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE game_id=$1 AND realm_id=$2`, fixture.gameID, fixture.realmID).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox rows after missing wallet: %v", err)
	}
	if operationCount != 0 || ledgerCount != 0 || outboxCount != 0 {
		t.Fatalf("failed settlement left operation/ledger/outbox rows %d/%d/%d", operationCount, ledgerCount, outboxCount)
	}
}

func TestEconomyRejectsUnknownRealmAndCharacter(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	service := economy.New(store.db)
	unknownRealm := fixture.command("unknown-realm", 1)
	unknownRealm.RealmID = "missing-realm"
	if _, err := service.Settle(ctx, unknownRealm); !errors.Is(err, economy.ErrOwnerEpochMismatch) {
		t.Fatalf("unknown realm settlement error = %v, want ErrOwnerEpochMismatch", err)
	}
	unknownCharacter := fixture.command("unknown-character", 1)
	unknownCharacter.CharacterID = "missing-character"
	if _, err := service.Settle(ctx, unknownCharacter); !errors.Is(err, economy.ErrActorNotAuthorized) {
		t.Fatalf("unknown character settlement error = %v, want ErrActorNotAuthorized", err)
	}
	assertEconomyState(t, ctx, store, fixture, 0, 0, 0)
}

func TestEconomyActorCannotRewardAnotherCharactersWallet(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	command := fixture.command("unauthorized-reward", 3)
	command.ActorSubject = "different-actor"
	if _, err := economy.New(store.db).Settle(ctx, command); !errors.Is(err, economy.ErrActorNotAuthorized) {
		t.Fatalf("reward with a non-owner actor error = %v, want ErrActorNotAuthorized", err)
	}
	assertEconomyState(t, ctx, store, fixture, 0, 0, 0)
}

func TestEconomyPostgreSQLCommitRejectionIsDefiniteAndRollsBack(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	if _, err := store.db.ExecContext(ctx, `
CREATE FUNCTION reject_reward_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected deferred commit rejection'; END $$;
CREATE CONSTRAINT TRIGGER reject_reward_commit_trigger AFTER INSERT ON outbox
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_reward_commit()`); err != nil {
		t.Fatalf("install deferred commit rejection: %v", err)
	}
	t.Cleanup(func() {
		_, err := store.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS reject_reward_commit_trigger ON outbox; DROP FUNCTION IF EXISTS reject_reward_commit()`)
		if err != nil {
			t.Errorf("remove deferred commit rejection: %v", err)
		}
	})
	command := fixture.command("reward-commit-rejected", 8)
	if _, err := economy.New(store.db).Settle(ctx, command); err == nil || errors.Is(err, economy.ErrOutcomeUnknown) {
		t.Fatalf("PostgreSQL definite commit rejection error = %v, want known non-pending failure", err)
	}
	assertEconomyState(t, ctx, store, fixture, 0, 0, 0)
}

func TestEconomyCommitConnectionLossRemainsPendingUntilSameKeyRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	const advisoryKey = int64(71826007)
	if _, err := store.db.ExecContext(ctx, `
CREATE FUNCTION wait_for_reward_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM pg_advisory_xact_lock(71826007); RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER wait_for_reward_commit_trigger AFTER INSERT ON outbox
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION wait_for_reward_commit()`); err != nil {
		t.Fatalf("install deferred commit barrier: %v", err)
	}
	t.Cleanup(func() {
		_, err := store.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS wait_for_reward_commit_trigger ON outbox; DROP FUNCTION IF EXISTS wait_for_reward_commit()`)
		if err != nil {
			t.Errorf("remove deferred commit barrier: %v", err)
		}
	})

	locker, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve commit barrier connection: %v", err)
	}
	defer locker.Close()
	if _, err := locker.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryKey); err != nil {
		t.Fatalf("hold commit barrier: %v", err)
	}
	defer locker.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryKey)

	command := fixture.command("reward-unknown-commit", 13)
	settlementResult := make(chan error, 1)
	go func() {
		_, err := economy.New(store.db).Settle(ctx, command)
		settlementResult <- err
	}()
	backendPID := awaitLockWaitingBackend(t, ctx, store)
	var terminated bool
	if err := store.db.QueryRowContext(ctx, `SELECT pg_terminate_backend($1)`, backendPID).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate blocked commit backend %d: terminated=%v error=%v", backendPID, terminated, err)
	}
	select {
	case err := <-settlementResult:
		if !errors.Is(err, economy.ErrOutcomeUnknown) {
			t.Fatalf("commit transport failure = %v, want pending ErrOutcomeUnknown", err)
		}
	case <-ctx.Done():
		t.Fatal("settlement did not return after test terminated its backend")
	}
	if _, err := locker.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, advisoryKey); err != nil {
		t.Fatalf("release commit barrier before recovery: %v", err)
	}

	service := economy.New(store.db)
	if _, err := service.Recover(ctx, command); !errors.Is(err, economy.ErrOperationNotFound) {
		t.Fatalf("unknown outcome lookup error = %v, want unresolved ErrOperationNotFound", err)
	}
	assertEconomyState(t, ctx, store, fixture, 0, 0, 0)
	if _, err := service.Settle(ctx, command); err != nil {
		t.Fatalf("retry using the same unique key after prior backend abort: %v", err)
	}
	assertEconomyState(t, ctx, store, fixture, 13, 1, 1)
}

func TestEconomyCommitAcknowledgmentLossRecoversCommittedReward(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	var loseAcknowledgment atomic.Bool
	loseAcknowledgment.Store(true)
	var databaseName string
	if err := store.db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read isolated settlement database name: %v", err)
	}
	dsn, err := databaseURLForTestDatabase(os.Getenv("TBCG_TEST_DATABASE_URL"), databaseName)
	if err != nil {
		t.Fatalf("build isolated settlement database URL: %v", err)
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse isolated PostgreSQL test database URL: %v", err)
	}
	db := sql.OpenDB(&commitAcknowledgmentLossConnector{
		base: stdlib.GetDefaultDriver(), dsn: config.ConnString(), loseAcknowledgment: &loseAcknowledgment,
	})
	defer db.Close()
	service := economy.New(db)
	command := fixture.command("reward-commit-ack-lost", 13)
	if _, err := service.Settle(ctx, command); !errors.Is(err, economy.ErrOutcomeUnknown) {
		t.Fatalf("successful server commit with lost acknowledgment error = %v, want pending ErrOutcomeUnknown", err)
	}
	result, err := service.Recover(ctx, command)
	if err != nil || result != (economy.Result{Balance: 13, Amount: 13}) {
		t.Fatalf("recover committed reward after lost acknowledgment = %+v, error %v", result, err)
	}
	retry, err := service.Settle(ctx, command)
	if err != nil || retry != result {
		t.Fatalf("same-key retry after lost acknowledgment = %+v, error %v; want persisted result %+v", retry, err, result)
	}
	assertEconomyState(t, ctx, store, fixture, 13, 1, 1)
}

type commitAcknowledgmentLossConnector struct {
	base               driver.Driver
	dsn                string
	loseAcknowledgment *atomic.Bool
}

func (c *commitAcknowledgmentLossConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return commitAcknowledgmentLossConn{Conn: conn, loseAcknowledgment: c.loseAcknowledgment}, nil
}

func (c *commitAcknowledgmentLossConnector) Driver() driver.Driver { return c }

func (c *commitAcknowledgmentLossConnector) Open(name string) (driver.Conn, error) {
	conn, err := c.base.Open(name)
	if err != nil {
		return nil, err
	}
	return commitAcknowledgmentLossConn{Conn: conn, loseAcknowledgment: c.loseAcknowledgment}, nil
}

type commitAcknowledgmentLossConn struct {
	driver.Conn
	loseAcknowledgment *atomic.Bool
}

func (c commitAcknowledgmentLossConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c commitAcknowledgmentLossConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("pgx driver connection does not support BeginTx")
	}
	tx, err := beginner.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return commitAcknowledgmentLossTx{Tx: tx, loseAcknowledgment: c.loseAcknowledgment}, nil
}

type commitAcknowledgmentLossTx struct {
	driver.Tx
	loseAcknowledgment *atomic.Bool
}

func (tx commitAcknowledgmentLossTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if tx.loseAcknowledgment.CompareAndSwap(true, false) {
		return errors.New("injected lost PostgreSQL commit acknowledgment")
	}
	return nil
}

func awaitLockWaitingBackend(t *testing.T, ctx context.Context, store *Store) int {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		err := store.db.QueryRowContext(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND wait_event_type='Lock' AND pid<>pg_backend_pid() ORDER BY query_start LIMIT 1`).Scan(&pid)
		if err == nil {
			return pid
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("find commit backend waiting on deferred trigger: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("settlement backend did not reach deferred commit barrier")
		case <-ticker.C:
		}
	}
}

func TestEconomyConcurrentDuplicateDeliveryCreditsOnce(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	command := fixture.command("reward-race", 11)
	service := economy.New(store.db)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := service.Settle(ctx, command)
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent duplicate settlement: %v", err)
		}
	}
	assertEconomyState(t, ctx, store, fixture, 11, 1, 1)
}

func TestEconomyOutboxRetriesAreIdempotent(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	service := economy.New(store.db)
	if _, err := service.Settle(ctx, fixture.command("reward-outbox", 4)); err != nil {
		t.Fatalf("settle reward with outbox event: %v", err)
	}

	prepareOutboxEffectTable(t, ctx, store)
	consumer := "wallet-cache"
	handler := recordOutboxEffect
	claim, err := service.ClaimOutbox(ctx, consumer, 10*time.Second)
	if err != nil {
		t.Fatalf("claim outbox message: %v", err)
	}
	if claim == nil || claim.EventID == "" {
		t.Fatal("expected one claimed outbox message")
	}
	if processed, err := service.ProcessOutbox(ctx, "another-consumer", claim.EventID, claim.ClaimToken, handler); err == nil || processed {
		t.Fatalf("unleased consumer processed outbox event: processed=%v error=%v", processed, err)
	}
	processed, err := service.ProcessOutbox(ctx, consumer, claim.EventID, claim.ClaimToken, handler)
	if err != nil || !processed {
		t.Fatalf("process first outbox delivery: processed=%v error=%v", processed, err)
	}
	processed, err = service.ProcessOutbox(ctx, consumer, claim.EventID, claim.ClaimToken, handler)
	if err != nil || processed {
		t.Fatalf("process duplicate outbox delivery: processed=%v error=%v, want no-op", processed, err)
	}
	var delivered bool
	if err := store.db.QueryRowContext(ctx, `SELECT delivered_at IS NOT NULL FROM outbox WHERE event_id = $1`, claim.EventID).Scan(&delivered); err != nil {
		t.Fatalf("read outbox delivery state: %v", err)
	}
	if !delivered {
		t.Fatal("outbox event was not marked delivered")
	}
	var effectCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM economy_consumer_effects WHERE event_id=$1`, claim.EventID).Scan(&effectCount); err != nil {
		t.Fatalf("read idempotent consumer side effect: %v", err)
	}
	if effectCount != 1 {
		t.Fatalf("consumer side-effect count = %d, want 1", effectCount)
	}
}

func TestEconomyExpiredOutboxLeaseCannotBeAcknowledgedByStaleWorker(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	service := economy.New(store.db)
	prepareOutboxEffectTable(t, ctx, store)
	if _, err := service.Settle(ctx, fixture.command("reward-lease", 5)); err != nil {
		t.Fatalf("settle reward: %v", err)
	}
	first, err := service.ClaimOutbox(ctx, "wallet-cache", 25*time.Millisecond)
	if err != nil || first == nil {
		t.Fatalf("claim first outbox lease: message=%+v error=%v", first, err)
	}
	failedHandler := func(ctx context.Context, tx *sql.Tx, message economy.OutboxMessage) error {
		if err := recordOutboxEffect(ctx, tx, message); err != nil {
			return err
		}
		return errors.New("injected consumer failure")
	}
	if processed, err := service.ProcessOutbox(ctx, "wallet-cache", first.EventID, first.ClaimToken, failedHandler); err == nil || processed {
		t.Fatalf("failing consumer side effect returned processed=%v error=%v", processed, err)
	}
	var rolledBackEffects int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM economy_consumer_effects WHERE event_id=$1`, first.EventID).Scan(&rolledBackEffects); err != nil || rolledBackEffects != 0 {
		t.Fatalf("consumer side effect survived callback rollback: count=%d error=%v", rolledBackEffects, err)
	}
	time.Sleep(40 * time.Millisecond)
	second, err := service.ClaimOutbox(ctx, "wallet-cache", time.Second)
	if err != nil || second == nil {
		t.Fatalf("reclaim expired outbox lease: message=%+v error=%v", second, err)
	}
	if first.ClaimToken == second.ClaimToken {
		t.Fatal("outbox lease token was reused after lease expiration")
	}
	if processed, err := service.ProcessOutbox(ctx, "wallet-cache", first.EventID, first.ClaimToken, recordOutboxEffect); err == nil || processed {
		t.Fatalf("stale lease acknowledged redelivered event: processed=%v error=%v", processed, err)
	}
	if processed, err := service.ProcessOutbox(ctx, "wallet-cache", second.EventID, second.ClaimToken, recordOutboxEffect); err != nil || !processed {
		t.Fatalf("current lease acknowledgment: processed=%v error=%v", processed, err)
	}
}

type economyFixture struct {
	gameID      string
	realmID     string
	characterID string
	actor       string
	epoch       int64
}

func newEconomyFixture(t *testing.T, ctx context.Context, store *Store) economyFixture {
	t.Helper()
	fixture := economyFixture{
		gameID:      randomTestIdentifier(t, "economy-game-"),
		realmID:     randomTestIdentifier(t, "economy-realm-"),
		characterID: randomTestIdentifier(t, "economy-character-"),
		actor:       randomTestIdentifier(t, "economy-actor-"),
		epoch:       7,
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO games (game_id, display_name) VALUES ($1, $2)`, []any{fixture.gameID, "经济测试"}},
		{`INSERT INTO realms (game_id, realm_id, display_name) VALUES ($1, $2, $3)`, []any{fixture.gameID, fixture.realmID, "测试 Realm"}},
		{`INSERT INTO owner_guards (game_id, realm_id, owner_epoch) VALUES ($1, $2, $3)`, []any{fixture.gameID, fixture.realmID, fixture.epoch}},
		{`INSERT INTO characters (game_id, realm_id, character_id, owner_subject, instance_schema_version) VALUES ($1, $2, $3, $4, 1)`, []any{fixture.gameID, fixture.realmID, fixture.characterID, fixture.actor}},
		{`INSERT INTO wallets (game_id, realm_id, character_id, currency_code, balance) VALUES ($1, $2, $3, 'gold', 0)`, []any{fixture.gameID, fixture.realmID, fixture.characterID}},
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("prepare economy integration fixture: %v", err)
		}
	}
	return fixture
}

func (f economyFixture) command(key string, amount int64) economy.Command {
	return economy.Command{
		GameID: f.gameID, RealmID: f.realmID, ActorSubject: f.actor,
		Operation: "quest.reward", IdempotencyKey: key, CharacterID: f.characterID,
		CurrencyCode: "gold", Amount: amount, OwnerEpoch: f.epoch,
		EventType: "economy.rewarded",
	}
}

func assertEconomyState(t *testing.T, ctx context.Context, store *Store, f economyFixture, balance, ledgerCount, outboxCount int64) {
	t.Helper()
	var actualBalance, actualLedger, actualResults, actualOutbox int64
	if err := store.db.QueryRowContext(ctx, `SELECT balance FROM wallets WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND currency_code='gold'`, f.gameID, f.realmID, f.characterID).Scan(&actualBalance); err != nil {
		t.Fatalf("read economy balance: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM wallet_ledger WHERE game_id=$1 AND realm_id=$2 AND character_id=$3`, f.gameID, f.realmID, f.characterID).Scan(&actualLedger); err != nil {
		t.Fatalf("count economy ledger rows: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM operation_results WHERE game_id=$1 AND realm_id=$2`, f.gameID, f.realmID).Scan(&actualResults); err != nil {
		t.Fatalf("count economy operation results: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE game_id=$1 AND realm_id=$2`, f.gameID, f.realmID).Scan(&actualOutbox); err != nil {
		t.Fatalf("count economy outbox rows: %v", err)
	}
	if actualBalance != balance || actualLedger != ledgerCount || actualResults != ledgerCount || actualOutbox != outboxCount {
		t.Fatalf("economy persisted balance/ledger/results/outbox = %d/%d/%d/%d, want %d/%d/%d/%d", actualBalance, actualLedger, actualResults, actualOutbox, balance, ledgerCount, ledgerCount, outboxCount)
	}
}

func prepareOutboxEffectTable(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE economy_consumer_effects (event_id uuid PRIMARY KEY, payload jsonb NOT NULL)`); err != nil {
		t.Fatalf("create integration consumer effect table: %v", err)
	}
}

func recordOutboxEffect(ctx context.Context, tx *sql.Tx, message economy.OutboxMessage) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO economy_consumer_effects (event_id,payload) VALUES ($1,$2::jsonb)`, message.EventID, message.Payload)
	return err
}
