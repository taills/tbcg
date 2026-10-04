package economy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"example.com/tbcg/server/internal/persistence"
)

const (
	eventTypeRewarded      = "economy.rewarded"
	eventTypeQuestRewarded = "economy.quest.rewarded"
)

var (
	ErrIdempotencyConflict = errors.New("economy: idempotency key payload conflict")
	ErrOwnerEpochMismatch  = errors.New("economy: owner epoch mismatch")
	ErrOperationNotFound   = errors.New("economy: operation result not found")
	ErrOutcomeUnknown      = errors.New("economy: settlement outcome unknown; operation remains pending")
	ErrInvalidCommand      = errors.New("economy: invalid settlement command")
	ErrActorNotAuthorized  = errors.New("economy: actor does not own reward character")
)

type Command struct {
	GameID         string
	RealmID        string
	ActorSubject   string
	Operation      string
	IdempotencyKey string
	CharacterID    string
	CurrencyCode   string
	Amount         int64
	OwnerEpoch     int64
	EventType      string
}

type Result struct {
	Balance int64 `json:"balance"`
	Amount  int64 `json:"amount"`
}

type OutboxMessage struct {
	EventID    string
	EventType  string
	Payload    json.RawMessage
	ClaimToken string
}

// OutboxHandler 只通过传入事务写入数据库，不执行网络或文件副作用。
type OutboxHandler func(context.Context, *sql.Tx, OutboxMessage) error

type Service struct {
	db *sql.DB
}

func New(db *sql.DB) *Service { return &Service{db: db} }

// Settle 始终校验当前 owner 与角色归属，包括幂等重放。
// 接管后的旧命令使用 Recover 只读核验；新 owner 重放同键时须使用当前 epoch。
func (s *Service) Settle(ctx context.Context, command Command) (Result, error) {
	if err := validateCommand(command); err != nil {
		return Result{}, err
	}
	payloadHash, err := commandHash(command)
	if err != nil {
		return Result{}, fmt.Errorf("hash settlement payload: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("begin reward transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockOwnerEpoch(ctx, tx, command); err != nil {
		return Result{}, err
	}
	if err := verifyCharacterOwner(ctx, tx, command); err != nil {
		return Result{}, err
	}

	existing, found, err := reserveOperation(ctx, tx, command, payloadHash)
	if err != nil {
		return Result{}, err
	}
	if found {
		if err := tx.Commit(); err != nil {
			return Result{}, fmt.Errorf("commit idempotent replay transaction: %w", err)
		}
		return existing, nil
	}
	result, err := applyReward(ctx, tx, command)
	if err != nil {
		return Result{}, err
	}
	if err := commitSettlement(tx); err != nil {
		return Result{}, err
	}
	return result, nil
}

func lockOwnerEpoch(ctx context.Context, tx *sql.Tx, command Command) error {
	var epoch int64
	err := tx.QueryRowContext(ctx, `SELECT owner_epoch FROM owner_guards WHERE game_id=$1 AND realm_id=$2 FOR SHARE`, command.GameID, command.RealmID).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lock realm owner guard: %w", ErrOwnerEpochMismatch)
	}
	if err != nil {
		return fmt.Errorf("lock realm owner guard: %w", err)
	}
	if epoch != command.OwnerEpoch {
		return fmt.Errorf("expected owner epoch %d, found %d: %w", command.OwnerEpoch, epoch, ErrOwnerEpochMismatch)
	}
	return nil
}

func verifyCharacterOwner(ctx context.Context, tx *sql.Tx, command Command) error {
	var ownerSubject string
	err := tx.QueryRowContext(ctx, `SELECT owner_subject FROM characters WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 FOR SHARE`, command.GameID, command.RealmID, command.CharacterID).Scan(&ownerSubject)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("find reward character: %w", ErrActorNotAuthorized)
	}
	if err != nil {
		return fmt.Errorf("lock reward character ownership: %w", err)
	}
	if ownerSubject != command.ActorSubject {
		return ErrActorNotAuthorized
	}
	return nil
}

func reserveOperation(ctx context.Context, tx *sql.Tx, command Command, payloadHash [sha256.Size]byte) (Result, bool, error) {
	initialResult, err := json.Marshal(Result{})
	if err != nil {
		return Result{}, false, fmt.Errorf("encode initial operation result: %w", err)
	}
	inserted, err := tx.ExecContext(ctx, `
INSERT INTO operation_results (game_id,realm_id,actor_subject,operation_name,idempotency_key,payload_hash,result)
VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb) ON CONFLICT DO NOTHING`, command.GameID, command.RealmID, command.ActorSubject, command.Operation, command.IdempotencyKey, payloadHash[:], initialResult)
	if err != nil {
		return Result{}, false, fmt.Errorf("reserve idempotency key: %w", err)
	}
	rows, err := inserted.RowsAffected()
	if err != nil {
		return Result{}, false, fmt.Errorf("read idempotency reservation result: %w", err)
	}
	if rows == 1 {
		return Result{}, false, nil
	}
	if rows != 0 {
		return Result{}, false, fmt.Errorf("reserve idempotency key affected unexpected row count %d", rows)
	}
	result, err := findExistingOperation(ctx, tx, command, payloadHash)
	return result, true, err
}

func findExistingOperation(ctx context.Context, tx *sql.Tx, command Command, payloadHash [sha256.Size]byte) (Result, error) {
	var storedHash, storedResult []byte
	err := tx.QueryRowContext(ctx, `SELECT payload_hash,result FROM operation_results WHERE game_id=$1 AND realm_id=$2 AND actor_subject=$3 AND operation_name=$4 AND idempotency_key=$5`, command.GameID, command.RealmID, command.ActorSubject, command.Operation, command.IdempotencyKey).Scan(&storedHash, &storedResult)
	if err != nil {
		return Result{}, fmt.Errorf("read existing idempotency result: %w", err)
	}
	if !equalBytes(storedHash, payloadHash[:]) {
		return Result{}, fmt.Errorf("idempotency key reused with another payload: %w", ErrIdempotencyConflict)
	}
	var result Result
	if err := json.Unmarshal(storedResult, &result); err != nil {
		return Result{}, fmt.Errorf("decode stored operation result: %w", err)
	}
	return result, nil
}

func applyReward(ctx context.Context, tx *sql.Tx, command Command) (Result, error) {
	var balance int64
	err := tx.QueryRowContext(ctx, `UPDATE wallets SET balance=balance+$5,updated_at=now()
WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND currency_code=$4 RETURNING balance`, command.GameID, command.RealmID, command.CharacterID, command.CurrencyCode, command.Amount).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, errors.New("update reward wallet: wallet not found")
	}
	if err != nil {
		return Result{}, fmt.Errorf("update reward wallet: %w", err)
	}
	result := Result{Balance: balance, Amount: command.Amount}
	if err := saveOperationResult(ctx, tx, command, result); err != nil {
		return Result{}, err
	}
	if err := insertRewardLedger(ctx, tx, command); err != nil {
		return Result{}, err
	}
	if err := enqueueRewardEvent(ctx, tx, command, result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func saveOperationResult(ctx context.Context, tx *sql.Tx, command Command, result Result) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode operation result: %w", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE operation_results SET result=$6::jsonb WHERE game_id=$1 AND realm_id=$2 AND actor_subject=$3 AND operation_name=$4 AND idempotency_key=$5`, command.GameID, command.RealmID, command.ActorSubject, command.Operation, command.IdempotencyKey, encoded)
	if err != nil {
		return fmt.Errorf("store operation result: %w", err)
	}
	return nil
}

func insertRewardLedger(ctx context.Context, tx *sql.Tx, command Command) error {
	ledgerID, err := randomUUID()
	if err != nil {
		return fmt.Errorf("create reward ledger ID: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO wallet_ledger (ledger_id,game_id,realm_id,character_id,currency_code,actor_subject,operation_name,idempotency_key,line_item_id,amount) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'reward',$9)`, ledgerID, command.GameID, command.RealmID, command.CharacterID, command.CurrencyCode, command.ActorSubject, command.Operation, command.IdempotencyKey, command.Amount)
	if err != nil {
		return fmt.Errorf("insert reward ledger: %w", err)
	}
	return nil
}

func enqueueRewardEvent(ctx context.Context, tx *sql.Tx, command Command, result Result) error {
	eventID, err := randomUUID()
	if err != nil {
		return fmt.Errorf("create outbox event ID: %w", err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode outbox payload: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox (event_id,game_id,realm_id,actor_subject,operation_name,idempotency_key,event_key,event_type,payload) VALUES ($1,$2,$3,$4,$5,$6,'reward',$7,$8::jsonb)`, eventID, command.GameID, command.RealmID, command.ActorSubject, command.Operation, command.IdempotencyKey, command.EventType, payload)
	if err != nil {
		return fmt.Errorf("insert reward outbox event: %w", err)
	}
	return nil
}

func commitSettlement(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		if !isCommitOutcomeUnknown(err) {
			return fmt.Errorf("PostgreSQL rejected reward transaction commit: %w", err)
		}
		return fmt.Errorf("commit reward transaction: %w", errors.Join(ErrOutcomeUnknown, persistence.ErrUnknown, err))
	}
	return nil
}

func isCommitOutcomeUnknown(err error) bool {
	return persistence.IsCommitOutcomeUnknown(err)
}

// Recover 按完整作用域键与载荷哈希只读核验已提交结果，不要求旧命令的 epoch 仍有效。
// 调用方须先完成身份授权；缺少记录仍表示结果未确定，不构成重发奖励的依据。
func (s *Service) Recover(ctx context.Context, command Command) (Result, error) {
	if err := validateCommand(command); err != nil {
		return Result{}, err
	}
	hash, err := commandHash(command)
	if err != nil {
		return Result{}, fmt.Errorf("hash settlement payload: %w", err)
	}
	var storedHash, storedResult []byte
	err = s.db.QueryRowContext(ctx, `SELECT payload_hash, result FROM operation_results WHERE game_id=$1 AND realm_id=$2 AND actor_subject=$3 AND operation_name=$4 AND idempotency_key=$5`, command.GameID, command.RealmID, command.ActorSubject, command.Operation, command.IdempotencyKey).Scan(&storedHash, &storedResult)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrOperationNotFound
	}
	if err != nil {
		return Result{}, fmt.Errorf("query persisted operation result: %w", err)
	}
	if !equalBytes(storedHash, hash[:]) {
		return Result{}, ErrIdempotencyConflict
	}
	var result Result
	if err := json.Unmarshal(storedResult, &result); err != nil {
		return Result{}, fmt.Errorf("decode persisted operation result: %w", err)
	}
	return result, nil
}

func (s *Service) ClaimOutbox(ctx context.Context, consumer string, lease time.Duration) (*OutboxMessage, error) {
	if strings.TrimSpace(consumer) == "" || lease <= 0 {
		return nil, errors.New("claim outbox: consumer and positive lease duration are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var message OutboxMessage
	err = tx.QueryRowContext(ctx, `SELECT event_id::text, event_type, payload FROM outbox
WHERE delivered_at IS NULL AND available_at <= now() AND (lease_expires_at IS NULL OR lease_expires_at <= now())
ORDER BY created_at, event_id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&message.EventID, &message.EventType, &message.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select pending outbox event: %w", err)
	}
	token, err := randomUUID()
	if err != nil {
		return nil, fmt.Errorf("create outbox lease token: %w", err)
	}
	leaseToken := consumer + "|" + token
	if _, err := tx.ExecContext(ctx, `UPDATE outbox SET lease_owner=$2, lease_expires_at=now()+$3::interval, attempts=attempts+1 WHERE event_id=$1`, message.EventID, leaseToken, lease.String()); err != nil {
		return nil, fmt.Errorf("lease outbox event: %w", err)
	}
	message.ClaimToken = leaseToken
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit outbox claim: %w", err)
	}
	return &message, nil
}

// ProcessOutbox 在事务中应用消费者写入、记录凭据并标记事件已投递。
func (s *Service) ProcessOutbox(ctx context.Context, consumer, eventID, claimToken string, handler OutboxHandler) (bool, error) {
	if strings.TrimSpace(consumer) == "" || strings.TrimSpace(eventID) == "" || strings.TrimSpace(claimToken) == "" || handler == nil {
		return false, errors.New("process outbox: consumer, event ID, claim token, and handler are required")
	}
	if !strings.HasPrefix(claimToken, consumer+"|") {
		return false, errors.New("process outbox: claim token belongs to another consumer")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin outbox processing: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	message, err := loadLeasedOutboxMessage(ctx, tx, eventID, claimToken)
	if errors.Is(err, sql.ErrNoRows) {
		done, receiptErr := hasConsumerReceipt(ctx, tx, consumer, eventID)
		if receiptErr != nil {
			return false, receiptErr
		}
		if done {
			return false, nil
		}
		return false, errors.New("process outbox: event is missing, expired, or leased to another worker")
	}
	if err != nil {
		return false, fmt.Errorf("load leased outbox event: %w", err)
	}
	inserted, err := insertConsumerReceipt(ctx, tx, consumer, eventID)
	if err != nil {
		return false, err
	}
	if !inserted {
		return false, nil
	}
	if err := handler(ctx, tx, message); err != nil {
		return false, fmt.Errorf("apply outbox consumer effect: %w", err)
	}
	if err := markOutboxDelivered(ctx, tx, eventID, claimToken); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit outbox processing: %w", err)
	}
	return true, nil
}

func loadLeasedOutboxMessage(ctx context.Context, tx *sql.Tx, eventID, claimToken string) (OutboxMessage, error) {
	var message OutboxMessage
	err := tx.QueryRowContext(ctx, `SELECT event_id::text,event_type,payload FROM outbox WHERE event_id=$1 AND lease_owner=$2 AND delivered_at IS NULL AND lease_expires_at>now() FOR UPDATE`, eventID, claimToken).Scan(&message.EventID, &message.EventType, &message.Payload)
	if err != nil {
		return OutboxMessage{}, err
	}
	message.ClaimToken = claimToken
	return message, nil
}

func hasConsumerReceipt(ctx context.Context, tx *sql.Tx, consumer, eventID string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM outbox_consumer_receipts WHERE consumer_name=$1 AND event_id=$2)`, consumer, eventID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check outbox consumer receipt: %w", err)
	}
	return exists, nil
}

func insertConsumerReceipt(ctx context.Context, tx *sql.Tx, consumer, eventID string) (bool, error) {
	var inserted int
	err := tx.QueryRowContext(ctx, `INSERT INTO outbox_consumer_receipts (consumer_name,event_id) VALUES ($1,$2) ON CONFLICT DO NOTHING RETURNING 1`, consumer, eventID).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("record outbox consumer receipt: %w", err)
	}
	return true, nil
}

func markOutboxDelivered(ctx context.Context, tx *sql.Tx, eventID, claimToken string) error {
	result, err := tx.ExecContext(ctx, `UPDATE outbox SET delivered_at=now(), lease_owner=NULL, lease_expires_at=NULL WHERE event_id=$1 AND lease_owner=$2 AND delivered_at IS NULL`, eventID, claimToken)
	if err != nil {
		return fmt.Errorf("mark outbox event delivered: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read outbox delivery update result: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("mark outbox event delivered: event %q not found", eventID)
	}
	return nil
}

func validateCommand(command Command) error {
	if strings.TrimSpace(command.GameID) == "" || strings.TrimSpace(command.RealmID) == "" || strings.TrimSpace(command.ActorSubject) == "" || strings.TrimSpace(command.Operation) == "" || strings.TrimSpace(command.IdempotencyKey) == "" || strings.TrimSpace(command.CharacterID) == "" || strings.TrimSpace(command.CurrencyCode) == "" || !supportedRewardEventType(command.EventType) || command.Amount <= 0 || command.OwnerEpoch < 0 {
		return ErrInvalidCommand
	}
	return nil
}

func commandHash(command Command) ([sha256.Size]byte, error) {
	canonical := struct {
		CharacterID  string `json:"character_id"`
		CurrencyCode string `json:"currency_code"`
		Amount       int64  `json:"amount"`
		EventType    string `json:"event_type"`
	}{command.CharacterID, command.CurrencyCode, command.Amount, command.EventType}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(payload), nil
}

func supportedRewardEventType(eventType string) bool {
	switch eventType {
	case eventTypeRewarded, eventTypeQuestRewarded:
		return true
	default:
		return false
	}
}

func randomUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
