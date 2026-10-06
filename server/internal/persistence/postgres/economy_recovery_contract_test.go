package postgres

import (
	"errors"
	"testing"

	"github.com/taills/tbcg/server/internal/economy"
)

func TestEconomyRecoveryAndIdempotentReplayRespectOwnerEpochContract(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	fixture := newEconomyFixture(t, ctx, store)
	service := economy.New(store.db)
	oldOwnerCommand := fixture.command("owner-recovery-contract", 13)

	committed, err := service.Settle(ctx, oldOwnerCommand)
	if err != nil {
		t.Fatalf("settle reward as current owner: %v", err)
	}
	if committed != (economy.Result{Balance: 13, Amount: 13}) {
		t.Fatalf("initial settlement result = %+v, want balance and amount 13", committed)
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE owner_guards SET owner_epoch=owner_epoch+1 WHERE game_id=$1 AND realm_id=$2`, fixture.gameID, fixture.realmID); err != nil {
		t.Fatalf("advance owner epoch for takeover: %v", err)
	}
	if _, err := service.Settle(ctx, oldOwnerCommand); !errors.Is(err, economy.ErrOwnerEpochMismatch) {
		t.Fatalf("old owner settlement error = %v, want ErrOwnerEpochMismatch", err)
	}

	recovered, err := service.Recover(ctx, oldOwnerCommand)
	if err != nil || recovered != committed {
		t.Fatalf("read committed result using old owner command = %+v, error %v; want %+v", recovered, err, committed)
	}

	newOwnerCommand := oldOwnerCommand
	newOwnerCommand.OwnerEpoch++
	replayed, err := service.Settle(ctx, newOwnerCommand)
	if err != nil || replayed != committed {
		t.Fatalf("new owner same-key settlement replay = %+v, error %v; want original %+v", replayed, err, committed)
	}
	assertEconomyState(t, ctx, store, fixture, 13, 1, 1)
}
