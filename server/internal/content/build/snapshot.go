package build

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	contentassets "github.com/taills/tbcg/server/internal/content/assets"
	"github.com/taills/tbcg/server/internal/content/catalog"
	"github.com/taills/tbcg/server/internal/content/mapdata"
	"github.com/taills/tbcg/server/internal/content/schema"
)

const (
	SnapshotBuilderVersion = "game-snapshot-builder-v1"
	SnapshotScope          = "game_snapshot"
	maxSnapshotEntries     = 2000
)

// SnapshotEntry 是入队时冻结的一个已批准 revision。
type SnapshotEntry struct {
	ContentID   string          `json:"content_id"`
	ContentType string          `json:"content_type"`
	Revision    int32           `json:"revision"`
	BodyHash    string          `json:"body_hash"`
	Definition  json.RawMessage `json:"definition"`
}

// SnapshotInput 是完整 Game 快照构建的冻结输入。
type SnapshotInput struct {
	GameID            string                         `json:"game_id"`
	ReleaseID         string                         `json:"release_id"`
	ReleaseVersion    int                            `json:"release_version"`
	Profile           CompatibilityProfile           `json:"profile"`
	SchemaFingerprint string                         `json:"schema_fingerprint"`
	Entries           []SnapshotEntry                `json:"entries"`
	Assets            []contentassets.FrozenSnapshot `json:"assets"`
}

type snapshotAsset struct {
	AssetID string `json:"asset_id"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Data    string `json:"data_base64"`
}

type privateSnapshotEntry struct {
	ContentID   string          `json:"content_id"`
	ContentType string          `json:"content_type"`
	Revision    int32           `json:"revision"`
	BodyHash    string          `json:"body_hash"`
	Definition  json.RawMessage `json:"definition"`
}

// PrivateSnapshot 是服务端私有包（含 server 字段），只能由 runtime owner 读取。
type PrivateSnapshot struct {
	FormatVersion  int                    `json:"format_version"`
	Scope          string                 `json:"scope"`
	GameID         string                 `json:"game_id"`
	ReleaseID      string                 `json:"release_id"`
	ReleaseVersion int                    `json:"release_version"`
	Entries        []privateSnapshotEntry `json:"entries"`
	Assets         []snapshotAsset        `json:"assets"`
}

// PublicSnapshot 是客户端白名单包。
type PublicSnapshot struct {
	FormatVersion  int                   `json:"format_version"`
	Scope          string                `json:"scope"`
	GameID         string                `json:"game_id"`
	ReleaseID      string                `json:"release_id"`
	ReleaseVersion int                   `json:"release_version"`
	Entries        []catalog.PublicEntry `json:"entries"`
	Assets         []snapshotAsset       `json:"assets"`
}

// SnapshotManifest 描述快照 Release；数据库中 releases.manifest 保存同一 JSON。
type SnapshotManifest struct {
	FormatVersion         int                  `json:"format_version"`
	Scope                 string               `json:"scope"`
	GameID                string               `json:"game_id"`
	ReleaseID             string               `json:"release_id"`
	ReleaseVersion        int                  `json:"release_version"`
	CompatibilityProfile  CompatibilityProfile `json:"compatibility"`
	Capabilities          []string             `json:"capabilities"`
	SchemaFingerprint     string               `json:"schema_fingerprint"`
	BuilderVersion        string               `json:"builder_version"`
	ContentRevisions      []manifestRevision   `json:"content_revisions"`
	Assets                []manifestAsset      `json:"assets"`
	PublicArtifactSHA256  string               `json:"public_artifact_sha256"`
	PrivateArtifactSHA256 string               `json:"private_artifact_sha256"`
}

// BuildSnapshot 校验冻结输入的完整性、Schema 与跨内容引用，并生成确定性的公私包。
func BuildSnapshot(registry *schema.Registry, geometry *mapdata.Validator, input SnapshotInput) (Artifacts, error) {
	if registry == nil || geometry == nil {
		return Artifacts{}, errors.New("snapshot build: trusted validators are required")
	}
	if err := validateSnapshotIdentity(registry, input); err != nil {
		return Artifacts{}, err
	}
	entries := make([]catalog.Entry, 0, len(input.Entries))
	privateEntries := make([]privateSnapshotEntry, 0, len(input.Entries))
	revisions := make([]manifestRevision, 0, len(input.Entries))
	sorted := append([]SnapshotEntry(nil), input.Entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ContentID < sorted[j].ContentID })
	for _, entry := range sorted {
		canonical, err := canonicalJSON(entry.Definition)
		if err != nil {
			return Artifacts{}, fmt.Errorf("snapshot build: canonicalize %s: %w", entry.ContentID, err)
		}
		digest := sha256.Sum256(canonical)
		expected, err := hex.DecodeString(entry.BodyHash)
		if err != nil || subtle.ConstantTimeCompare(digest[:], expected) != 1 {
			return Artifacts{}, fmt.Errorf("snapshot build: %s body hash mismatch", entry.ContentID)
		}
		entries = append(entries, catalog.Entry{ContentID: entry.ContentID, ContentType: entry.ContentType, Revision: entry.Revision, Definition: canonical})
		privateEntries = append(privateEntries, privateSnapshotEntry{ContentID: entry.ContentID, ContentType: entry.ContentType, Revision: entry.Revision, BodyHash: entry.BodyHash, Definition: canonical})
		revisions = append(revisions, manifestRevision{ContentID: entry.ContentID, Revision: entry.Revision, BodyHash: entry.BodyHash})
	}
	loaded, issues := catalog.Load(input.GameID, entries, registry, geometry)
	if len(issues) != 0 {
		return Artifacts{}, fmt.Errorf("snapshot build: content validation failed: %s", issues[0].Error())
	}
	assets, manifestAssets, err := snapshotAssets(input, loaded)
	if err != nil {
		return Artifacts{}, err
	}
	publicEntries, err := catalog.PublicProjection(registry, entries)
	if err != nil {
		return Artifacts{}, fmt.Errorf("snapshot build: %w", err)
	}
	for i := range publicEntries {
		if publicEntries[i].Public, err = canonicalJSON(publicEntries[i].Public); err != nil {
			return Artifacts{}, fmt.Errorf("snapshot build: canonicalize public projection: %w", err)
		}
	}
	public, err := json.Marshal(PublicSnapshot{FormatVersion: packageFormatVersion, Scope: "public", GameID: input.GameID, ReleaseID: input.ReleaseID,
		ReleaseVersion: input.ReleaseVersion, Entries: publicEntries, Assets: assets})
	if err != nil {
		return Artifacts{}, fmt.Errorf("snapshot build: encode public bundle: %w", err)
	}
	private, err := json.Marshal(PrivateSnapshot{FormatVersion: packageFormatVersion, Scope: "private", GameID: input.GameID, ReleaseID: input.ReleaseID,
		ReleaseVersion: input.ReleaseVersion, Entries: privateEntries, Assets: assets})
	if err != nil {
		return Artifacts{}, fmt.Errorf("snapshot build: encode private bundle: %w", err)
	}
	manifest, err := json.Marshal(SnapshotManifest{
		FormatVersion: packageFormatVersion, Scope: SnapshotScope, GameID: input.GameID, ReleaseID: input.ReleaseID,
		ReleaseVersion: input.ReleaseVersion, CompatibilityProfile: input.Profile, Capabilities: snapshotCapabilities(entries),
		SchemaFingerprint: input.SchemaFingerprint, BuilderVersion: SnapshotBuilderVersion, ContentRevisions: revisions,
		Assets: manifestAssets, PublicArtifactSHA256: digestHex(public), PrivateArtifactSHA256: digestHex(private),
	})
	if err != nil {
		return Artifacts{}, fmt.Errorf("snapshot build: encode manifest: %w", err)
	}
	return Artifacts{Manifest: manifest, Public: public, Private: private}, nil
}

func validateSnapshotIdentity(registry *schema.Registry, input SnapshotInput) error {
	if !gameIDPattern.MatchString(input.GameID) || !releaseIDPattern.MatchString(input.ReleaseID) || input.ReleaseVersion < 1 {
		return errors.New("snapshot build: invalid Game or Release identity")
	}
	if input.Profile.ProfileVersion < 1 || input.Profile.ContentSchemaVersion < 1 || input.Profile.InstanceSchemaVersion < 1 ||
		input.Profile.RulesetVersion < 1 || input.Profile.ProtocolVersion < 1 || input.Profile.ClientMinBuild == "" || !validSHA256(input.Profile.AssetManifestSHA256) {
		return errors.New("snapshot build: incomplete compatibility profile")
	}
	if input.SchemaFingerprint != registry.Fingerprint() {
		return ErrSchemaFingerprintMismatch
	}
	if len(input.Entries) == 0 || len(input.Entries) > maxSnapshotEntries {
		return errors.New("snapshot build: entry count out of range")
	}
	return nil
}

func snapshotAssets(input SnapshotInput, loaded *catalog.Catalog) ([]snapshotAsset, []manifestAsset, error) {
	frozen := map[string]contentassets.Snapshot{}
	for _, raw := range input.Assets {
		snapshot, err := contentassets.NewFrozen(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("snapshot build: invalid frozen asset: %w", err)
		}
		manifestHash := sha256.Sum256(snapshot.Frozen().Manifest)
		if hex.EncodeToString(manifestHash[:]) != input.Profile.AssetManifestSHA256 {
			return nil, nil, errors.New("snapshot build: asset manifest differs from trusted Game profile")
		}
		frozen[snapshot.AssetID()] = snapshot
	}
	needed := map[string]bool{}
	for _, item := range loaded.Items {
		needed[item.Public.IconID] = true
	}
	ids := make([]string, 0, len(needed))
	for id := range needed {
		if _, ok := frozen[id]; !ok {
			return nil, nil, fmt.Errorf("snapshot build: icon %s missing from frozen licensed assets", id)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	assets := make([]snapshotAsset, 0, len(ids))
	manifest := make([]manifestAsset, 0, len(ids))
	for _, id := range ids {
		snapshot := frozen[id]
		assets = append(assets, snapshotAsset{AssetID: id, Path: snapshot.Path(), SHA256: snapshot.SHA256(), Data: base64.StdEncoding.EncodeToString(snapshot.Bytes())})
		manifest = append(manifest, manifestAsset{AssetID: id, Path: snapshot.Path(), SHA256: snapshot.SHA256(), Source: snapshot.Source(), License: snapshot.License()})
	}
	return assets, manifest, nil
}

func snapshotCapabilities(entries []catalog.Entry) []string {
	seen := map[string]bool{SnapshotScope: true}
	for _, entry := range entries {
		seen["content."+entry.ContentType] = true
	}
	out := make([]string, 0, len(seen))
	for capability := range seen {
		out = append(out, capability)
	}
	sort.Strings(out)
	return out
}

// DecodePrivateSnapshot 校验私有包哈希与身份并还原为目录条目。
func DecodePrivateSnapshot(data []byte, expectedSHA256, gameID, releaseID string) ([]catalog.Entry, error) {
	if digestHex(data) != expectedSHA256 {
		return nil, errors.New("private snapshot hash mismatch")
	}
	var snapshot PrivateSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode private snapshot: %w", err)
	}
	if snapshot.Scope != "private" || snapshot.GameID != gameID || snapshot.ReleaseID != releaseID || snapshot.FormatVersion != packageFormatVersion {
		return nil, errors.New("private snapshot identity mismatch")
	}
	entries := make([]catalog.Entry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries = append(entries, catalog.Entry{ContentID: entry.ContentID, ContentType: entry.ContentType, Revision: entry.Revision, Definition: entry.Definition})
	}
	return entries, nil
}

// HashDefinition 返回 canonical Definition 的 SHA-256（十六进制）。
func HashDefinition(definition []byte) (string, []byte, error) {
	canonical, err := canonicalJSON(definition)
	if err != nil {
		return "", nil, err
	}
	return digestHex(canonical), canonical, nil
}
