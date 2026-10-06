package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"example.com/tbcg/server/internal/admin"
	contentassets "example.com/tbcg/server/internal/content/assets"
	"example.com/tbcg/server/internal/content/schema"
)

type approvedBuildSeed struct {
	gameID      string
	publisherID string
	contentID   string
	frozenAsset contentassets.FrozenSnapshot
	canonical   []byte
	bodyHash    [32]byte
	validator   *schema.Validator
	request     admin.BuildRequest
}

// seedApprovedBuild provisions a Game, Publisher, trusted build profile, and one exactly approved item revision.
func seedApprovedBuild(t *testing.T, fixture *adminACLFixture, gamePrefix, publisherID, contentID string) approvedBuildSeed {
	t.Helper()
	gameID := randomTestIdentifier(t, gamePrefix)
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO games (game_id, display_name) VALUES ($1, 'Build test Game')`, gameID); err != nil {
		t.Fatalf("insert build Game: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO admin_users (admin_id, username, password_hash) VALUES ($1, $2, 'test-hash')`, publisherID, gameID+"-publisher"); err != nil {
		t.Fatalf("insert build Publisher: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO admin_role_bindings (game_id, admin_id, role) VALUES ($1, $2, 'Publisher')`, gameID, publisherID); err != nil {
		t.Fatalf("bind build Publisher role: %v", err)
	}
	assetSnapshot, err := contentassets.ReadIcon(os.DirFS("../../../../games/demo/assets"), "demo:icon/placeholder")
	if err != nil {
		t.Fatalf("read trusted asset snapshot: %v", err)
	}
	frozenAsset := assetSnapshot.Frozen()
	manifestHash := sha256.Sum256(frozenAsset.Manifest)
	if _, err := fixture.db.ExecContext(fixture.ctx, `
INSERT INTO game_build_profiles (game_id, content_schema_version, instance_schema_version,
    ruleset_version, protocol_version, client_min_build, asset_manifest_sha256)
VALUES ($1, 1, 1, 1, 1, '1.0.0', $2)`, gameID, manifestHash[:]); err != nil {
		t.Fatalf("provision build Game profile: %v", err)
	}
	definition, err := os.ReadFile("../../../../tests/fixtures/content/item.valid.json")
	if err != nil {
		t.Fatalf("read valid item Definition: %v", err)
	}
	definitionText := strings.ReplaceAll(string(definition), `"demo"`, fmt.Sprintf("%q", gameID))
	definitionText = strings.ReplaceAll(definitionText, `"demo:item/healing-potion"`, fmt.Sprintf("%q", contentID))
	definitionText = strings.Replace(definitionText, `"item/potion-small"`, `"demo:icon/placeholder"`, 1)
	definition = []byte(definitionText)
	canonical, err := canonicalJSON(definition)
	if err != nil {
		t.Fatalf("canonicalize approved Definition: %v", err)
	}
	bodyHash := sha256.Sum256(canonical)
	if _, err := fixture.db.ExecContext(fixture.ctx, `
INSERT INTO content_revisions (game_id, content_id, revision, schema_version, content_type, body)
VALUES ($1, $2, 1, 1, 'item', $3::jsonb)`, gameID, contentID, definition); err != nil {
		t.Fatalf("insert immutable build revision: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `
INSERT INTO revision_approvals (game_id, content_id, revision, body_hash, decision, reviewer_id)
VALUES ($1, $2, 1, $3, 'approved', $4)`, gameID, contentID, bodyHash[:], publisherID); err != nil {
		t.Fatalf("approve exact build revision: %v", err)
	}
	validator, err := schema.New(os.DirFS("../../../../contracts"))
	if err != nil {
		t.Fatalf("load trusted item schema: %v", err)
	}
	return approvedBuildSeed{
		gameID: gameID, publisherID: publisherID, contentID: contentID, frozenAsset: frozenAsset,
		canonical: canonical, bodyHash: bodyHash, validator: validator,
		request: admin.BuildRequest{ContentID: contentID, Revision: 1, BodyHash: hex.EncodeToString(bodyHash[:])},
	}
}
