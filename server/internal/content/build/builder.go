// Package build creates deterministic, immutable single-item content artifacts.
package build

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	contentassets "github.com/taills/tbcg/server/internal/content/assets"
	"github.com/taills/tbcg/server/internal/content/schema"
)

const (
	packageFormatVersion = 1
	maxDefinitionBytes   = 1 << 20
	BuilderVersion       = "item-builder-v1"
)

var (
	ErrSchemaFingerprintMismatch = errors.New("content build: trusted schema fingerprint mismatch")
	gameIDPattern                = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	releaseIDPattern             = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type CompatibilityProfile struct {
	ProfileVersion        int64  `json:"profile_version"`
	ContentSchemaVersion  int    `json:"content_schema_version"`
	InstanceSchemaVersion int    `json:"instance_schema_version"`
	RulesetVersion        int    `json:"ruleset_version"`
	ProtocolVersion       int    `json:"protocol_version"`
	ClientMinBuild        string `json:"client_min_build"`
	AssetManifestSHA256   string `json:"asset_manifest_sha256"`
}

type AssetSnapshot = contentassets.Snapshot

type Input struct {
	GameID            string
	ContentID         string
	Revision          int32
	BodyHash          []byte
	Definition        []byte
	ReleaseID         string
	ReleaseVersion    int
	Profile           CompatibilityProfile
	SchemaFingerprint string
	Asset             AssetSnapshot
}

type Artifacts struct {
	Manifest []byte
	Public   []byte
	Private  []byte
}

type definitionIdentity struct {
	GameID        string `json:"game_id"`
	ContentID     string `json:"content_id"`
	Revision      int32  `json:"revision"`
	SchemaVersion int    `json:"schema_version"`
	Public        struct {
		IconID string `json:"icon_id"`
	} `json:"public"`
}

type packageAsset struct {
	AssetID string `json:"asset_id"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Data    string `json:"data_base64"`
}

type packageItem struct {
	GameID    string          `json:"game_id"`
	ContentID string          `json:"content_id"`
	Revision  int32           `json:"revision"`
	Body      json.RawMessage `json:"body"`
}

type contentPackage struct {
	FormatVersion  int            `json:"format_version"`
	Scope          string         `json:"scope"`
	GameID         string         `json:"game_id"`
	ReleaseID      string         `json:"release_id"`
	ReleaseVersion int            `json:"release_version"`
	Items          []packageItem  `json:"items"`
	Assets         []packageAsset `json:"assets"`
}

type manifestAsset struct {
	AssetID string `json:"asset_id"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Source  string `json:"source"`
	License string `json:"license"`
}

type manifestRevision struct {
	ContentID string `json:"content_id"`
	Revision  int32  `json:"revision"`
	BodyHash  string `json:"body_hash"`
}

type releaseManifest struct {
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

// Build validates an approved immutable input and creates deterministic package bytes.
func Build(validator *schema.Validator, input Input) (Artifacts, error) {
	if validator == nil {
		return Artifacts{}, errors.New("content build: schema validator is required")
	}
	if err := validateInput(validator, input); err != nil {
		return Artifacts{}, err
	}
	identity, canonicalDefinition, publicDefinition, err := validatedDefinition(validator, input)
	if err != nil {
		return Artifacts{}, err
	}
	asset := packageAsset{
		AssetID: input.Asset.AssetID(),
		Path:    input.Asset.Path(),
		SHA256:  input.Asset.SHA256(),
		Data:    base64.StdEncoding.EncodeToString(input.Asset.Bytes()),
	}
	public, err := marshalPackage(input, packageItem{GameID: identity.GameID, ContentID: identity.ContentID, Revision: identity.Revision, Body: publicDefinition}, asset)
	if err != nil {
		return Artifacts{}, fmt.Errorf("encode public item package: %w", err)
	}
	private, err := marshalPackage(input, packageItem{GameID: identity.GameID, ContentID: identity.ContentID, Revision: identity.Revision, Body: canonicalDefinition}, asset)
	if err != nil {
		return Artifacts{}, fmt.Errorf("encode private item package: %w", err)
	}
	manifest, err := marshalManifest(input, identity, public, private)
	if err != nil {
		return Artifacts{}, err
	}
	return Artifacts{Manifest: manifest, Public: public, Private: private}, nil
}

func validateInput(validator *schema.Validator, input Input) error {
	if !gameIDPattern.MatchString(input.GameID) || !validContentID(input.ContentID) || input.Revision < 1 {
		return errors.New("content build: invalid immutable content identity")
	}
	if !releaseIDPattern.MatchString(input.ReleaseID) || input.ReleaseVersion < 1 {
		return errors.New("content build: invalid Release identity or version")
	}
	if input.Profile.ProfileVersion < 1 || input.Profile.ContentSchemaVersion < 1 || input.Profile.InstanceSchemaVersion < 1 ||
		input.Profile.RulesetVersion < 1 || input.Profile.ProtocolVersion < 1 || strings.TrimSpace(input.Profile.ClientMinBuild) == "" ||
		!validSHA256(input.Profile.AssetManifestSHA256) || input.Profile.AssetManifestSHA256 != strings.ToLower(input.Profile.AssetManifestSHA256) {
		return errors.New("content build: incomplete compatibility profile")
	}
	if len(input.Definition) == 0 || len(input.Definition) > maxDefinitionBytes || len(input.BodyHash) != sha256.Size {
		return errors.New("content build: invalid immutable Definition or body hash")
	}
	assetSnapshot, err := contentassets.NewFrozen(input.Asset.Frozen())
	if err != nil {
		return fmt.Errorf("content build: invalid licensed asset snapshot: %w", err)
	}
	manifestHash := sha256.Sum256(assetSnapshot.Frozen().Manifest)
	expectedManifestHash, err := hex.DecodeString(input.Profile.AssetManifestSHA256)
	if err != nil {
		return errors.New("content build: invalid trusted asset manifest fingerprint")
	}
	if subtle.ConstantTimeCompare(manifestHash[:], expectedManifestHash) != 1 {
		return errors.New("content build: asset manifest differs from trusted Game profile")
	}
	if !validSHA256(input.SchemaFingerprint) || input.SchemaFingerprint != validator.Fingerprint() {
		return ErrSchemaFingerprintMismatch
	}
	return nil
}

func validatedDefinition(validator *schema.Validator, input Input) (definitionIdentity, []byte, []byte, error) {
	var identity definitionIdentity
	if err := json.Unmarshal(input.Definition, &identity); err != nil {
		return identity, nil, nil, fmt.Errorf("decode approved Definition: %w", err)
	}
	if identity.GameID != input.GameID || identity.ContentID != input.ContentID || identity.Revision != input.Revision ||
		identity.SchemaVersion != input.Profile.ContentSchemaVersion || identity.Public.IconID != input.Asset.AssetID() {
		return identity, nil, nil, errors.New("content build: frozen identity, compatibility profile, or asset does not match Definition")
	}
	canonicalDefinition, err := canonicalJSON(input.Definition)
	if err != nil {
		return identity, nil, nil, fmt.Errorf("canonicalize approved Definition: %w", err)
	}
	actualBodyHash := sha256.Sum256(canonicalDefinition)
	if subtle.ConstantTimeCompare(actualBodyHash[:], input.BodyHash) != 1 {
		return identity, nil, nil, errors.New("content build: approved Definition body hash mismatch")
	}
	if issues := validator.ValidateDefinition(canonicalDefinition); len(issues) != 0 {
		return identity, nil, nil, fmt.Errorf("content build: approved Definition failed trusted schema validation: %s", issues[0].Code)
	}
	publicDefinition, issues := validator.ProjectPublic(canonicalDefinition)
	if len(issues) != 0 {
		return identity, nil, nil, fmt.Errorf("content build: public projection failed trusted schema validation: %s", issues[0].Code)
	}
	publicDefinition, err = canonicalJSON(publicDefinition)
	if err != nil {
		return identity, nil, nil, fmt.Errorf("canonicalize public projection: %w", err)
	}
	return identity, canonicalDefinition, publicDefinition, nil
}

func marshalPackage(input Input, item packageItem, asset packageAsset) ([]byte, error) {
	return json.Marshal(contentPackage{
		FormatVersion: packageFormatVersion, Scope: "single_item", GameID: input.GameID,
		ReleaseID: input.ReleaseID, ReleaseVersion: input.ReleaseVersion,
		Items: []packageItem{item}, Assets: []packageAsset{asset},
	})
}

func marshalManifest(input Input, identity definitionIdentity, public, private []byte) ([]byte, error) {
	manifest := releaseManifest{
		FormatVersion: packageFormatVersion, Scope: "single_item", GameID: input.GameID,
		ReleaseID: input.ReleaseID, ReleaseVersion: input.ReleaseVersion,
		CompatibilityProfile: input.Profile, Capabilities: []string{}, SchemaFingerprint: input.SchemaFingerprint,
		BuilderVersion:       BuilderVersion,
		ContentRevisions:     []manifestRevision{{ContentID: identity.ContentID, Revision: identity.Revision, BodyHash: hex.EncodeToString(input.BodyHash)}},
		Assets:               []manifestAsset{{AssetID: input.Asset.AssetID(), Path: input.Asset.Path(), SHA256: input.Asset.SHA256(), Source: input.Asset.Source(), License: input.Asset.License()}},
		PublicArtifactSHA256: digestHex(public), PrivateArtifactSHA256: digestHex(private),
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode deterministic Release manifest: %w", err)
	}
	return encoded, nil
}

func canonicalJSON(input []byte) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("trailing JSON data: %w", err)
	}
	return json.Marshal(value)
}

func validContentID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("._:/-", char)
		if !valid || index == 0 && !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func digestHex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
