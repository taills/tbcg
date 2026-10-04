package release

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"example.com/tbcg/server/internal/persistence"
)

const matchingRelease = `game_id=$1 AND release_id=$2 AND release_version=$3
AND content_schema_version=$4 AND instance_schema_version=$5 AND ruleset_version=$6
AND protocol_version=$7 AND client_min_build=$8 AND manifest=$9::jsonb
AND status IN ('built','activated')`

func releaseArguments(definition Definition, manifest []byte) []any {
	return []any{definition.GameID, definition.ReleaseID, definition.ReleaseVersion,
		definition.ContentSchemaVersion, definition.InstanceSchemaVersion,
		definition.RulesetVersion, definition.ProtocolVersion, definition.ClientMinBuild, manifest}
}

func (p *Publisher) activate(ctx context.Context, definition Definition, previous string, manifest []byte) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin release activation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockActivationOwner(ctx, tx, definition); err != nil {
		return err
	}
	if err := switchRealmRelease(ctx, tx, definition, previous); err != nil {
		return err
	}
	if err := markReleaseActivated(ctx, tx, definition, manifest); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		if !persistence.IsCommitOutcomeUnknown(err) {
			return fmt.Errorf("PostgreSQL rejected release activation commit: %w", err)
		}
		return fmt.Errorf("commit release activation: %w", errors.Join(ErrActivationOutcomeUnknown, persistence.ErrUnknown, err))
	}
	return nil
}

func lockActivationOwner(ctx context.Context, tx *sql.Tx, definition Definition) error {
	var epoch int64
	err := tx.QueryRowContext(ctx, `SELECT owner_epoch FROM owner_guards
WHERE game_id=$1 AND realm_id=$2 FOR SHARE`, definition.GameID, definition.RealmID).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOwnerEpochMismatch
	}
	if err != nil {
		return fmt.Errorf("lock release owner guard: %w", err)
	}
	if epoch != definition.OwnerEpoch {
		return ErrOwnerEpochMismatch
	}
	return nil
}

func switchRealmRelease(ctx context.Context, tx *sql.Tx, definition Definition, previous string) error {
	var current sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT active_release_id FROM realms
WHERE game_id=$1 AND realm_id=$2 FOR UPDATE`, definition.GameID, definition.RealmID).Scan(&current)
	if err != nil {
		return fmt.Errorf("lock active release pointer: %w", err)
	}
	// 并发发布同一不可变 Release 或确认丢失后的重试无需重复切换指针。
	if current.String == definition.ReleaseID {
		return nil
	}
	if current.String != previous {
		return ErrActivationConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE realms SET active_release_id=$3
WHERE game_id=$1 AND realm_id=$2`, definition.GameID, definition.RealmID, definition.ReleaseID)
	if err != nil {
		return fmt.Errorf("switch active release pointer: %w", err)
	}
	return nil
}

func markReleaseActivated(ctx context.Context, tx *sql.Tx, definition Definition, manifest []byte) error {
	result, err := tx.ExecContext(ctx, `UPDATE releases SET status='activated', activated_at=COALESCE(activated_at,now())
WHERE `+matchingRelease, releaseArguments(definition, manifest)...)
	if err != nil {
		return fmt.Errorf("mark release activated: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read release activation status update result: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("release definition or status changed before activation: %w", errors.Join(ErrReleaseConflict, ErrActivationConflict))
	}
	return nil
}
