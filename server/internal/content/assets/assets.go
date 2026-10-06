// Package assets loads allowlisted, licensed content assets into immutable snapshots.
package assets

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

const (
	manifestFilename = "manifest.json"
	manifestVersion  = 1
	maxManifestBytes = 1 << 20
	maxAssetBytes    = 1 << 20
	maxManifestRows  = 10000
	maxMetadataBytes = 512
)

type manifest struct {
	SchemaVersion int            `json:"schema_version"`
	Assets        []manifestItem `json:"assets"`
}

type manifestItem struct {
	AssetID string `json:"asset_id"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Source  string `json:"source"`
	License string `json:"license"`
}

// FrozenSnapshot is the serializable, immutable input needed to reverify an asset later.
type FrozenSnapshot struct {
	AssetID  string `json:"asset_id"`
	Manifest []byte `json:"manifest_bytes"`
	Data     []byte `json:"asset_bytes"`
}

// ErrInvalidAsset marks a manifest or asset that fails trusted validation, as opposed to an I/O failure.
var ErrInvalidAsset = errors.New("asset snapshot: invalid trusted asset")

// Snapshot can only be created by reading and validating an asset manifest or frozen copy.
type Snapshot struct {
	item     manifestItem
	manifest []byte
	data     []byte
}

// ReadIcon resolves exactly one manifest entry and snapshots its exact file bytes.
func ReadIcon(filesystem fs.FS, assetID string) (Snapshot, error) {
	if filesystem == nil || !validAssetID(assetID) {
		return Snapshot{}, fmt.Errorf("%w: invalid filesystem or asset identity", ErrInvalidAsset)
	}
	manifestBytes, err := fs.ReadFile(filesystem, manifestFilename)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read trusted asset manifest: %w", err)
	}
	parsed, err := parseManifest(manifestBytes)
	if err != nil {
		return Snapshot{}, err
	}
	item, err := findManifestItem(parsed, assetID)
	if err != nil {
		return Snapshot{}, err
	}
	data, err := fs.ReadFile(filesystem, item.Path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read allowlisted asset %q: %w", assetID, err)
	}
	return NewFrozen(FrozenSnapshot{AssetID: assetID, Manifest: manifestBytes, Data: data})
}

// NewFrozen verifies the stored manifest row and exact asset bytes before restoring a snapshot.
func NewFrozen(frozen FrozenSnapshot) (Snapshot, error) {
	if !validAssetID(frozen.AssetID) || len(frozen.Data) == 0 || len(frozen.Data) > maxAssetBytes {
		return Snapshot{}, fmt.Errorf("%w: invalid frozen identity or byte size", ErrInvalidAsset)
	}
	parsed, err := parseManifest(frozen.Manifest)
	if err != nil {
		return Snapshot{}, err
	}
	item, err := findManifestItem(parsed, frozen.AssetID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := validateBytes(item, frozen.Data); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		item:     item,
		manifest: append([]byte(nil), frozen.Manifest...),
		data:     append([]byte(nil), frozen.Data...),
	}, nil
}

// Frozen returns a deep copy suitable for a durable private job snapshot.
func (s Snapshot) Frozen() FrozenSnapshot {
	return FrozenSnapshot{
		AssetID:  s.item.AssetID,
		Manifest: append([]byte(nil), s.manifest...),
		Data:     append([]byte(nil), s.data...),
	}
}

func (s Snapshot) AssetID() string { return s.item.AssetID }
func (s Snapshot) Path() string    { return s.item.Path }
func (s Snapshot) SHA256() string  { return strings.ToLower(s.item.SHA256) }
func (s Snapshot) Source() string  { return s.item.Source }
func (s Snapshot) License() string { return s.item.License }
func (s Snapshot) Bytes() []byte   { return append([]byte(nil), s.data...) }

func parseManifest(data []byte) (manifest, error) {
	if len(data) == 0 || len(data) > maxManifestBytes {
		return manifest{}, fmt.Errorf("%w: invalid manifest size", ErrInvalidAsset)
	}
	var result manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return manifest{}, fmt.Errorf("%w: decode trusted asset manifest: %w", ErrInvalidAsset, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return manifest{}, fmt.Errorf("%w: manifest contains multiple JSON values", ErrInvalidAsset)
		}
		return manifest{}, fmt.Errorf("%w: trailing manifest data: %w", ErrInvalidAsset, err)
	}
	if result.SchemaVersion != manifestVersion || len(result.Assets) == 0 || len(result.Assets) > maxManifestRows {
		return manifest{}, fmt.Errorf("%w: unsupported manifest version or row count", ErrInvalidAsset)
	}
	return result, nil
}

func findManifestItem(value manifest, assetID string) (manifestItem, error) {
	var selected manifestItem
	matches := 0
	for _, item := range value.Assets {
		if item.AssetID != assetID {
			continue
		}
		selected = item
		matches++
	}
	if matches != 1 {
		return manifestItem{}, fmt.Errorf("%w: expected one manifest row for %q, got %d", ErrInvalidAsset, assetID, matches)
	}
	if !validAssetID(selected.AssetID) || !validManifestPath(selected.Path) ||
		strings.TrimSpace(selected.Source) == "" || len(selected.Source) > maxMetadataBytes ||
		strings.TrimSpace(selected.License) == "" || len(selected.License) > maxMetadataBytes {
		return manifestItem{}, fmt.Errorf("%w: allowlisted asset row has invalid path or attribution", ErrInvalidAsset)
	}
	declaredHash, err := hex.DecodeString(selected.SHA256)
	if err != nil || len(declaredHash) != sha256.Size {
		return manifestItem{}, fmt.Errorf("%w: allowlisted asset SHA-256 is invalid", ErrInvalidAsset)
	}
	return selected, nil
}

func validateBytes(item manifestItem, data []byte) error {
	if len(data) == 0 || len(data) > maxAssetBytes {
		return fmt.Errorf("%w: invalid asset byte size", ErrInvalidAsset)
	}
	declaredHash, err := hex.DecodeString(item.SHA256)
	if err != nil || len(declaredHash) != sha256.Size {
		return fmt.Errorf("%w: allowlisted asset SHA-256 is invalid", ErrInvalidAsset)
	}
	actualHash := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(actualHash[:], declaredHash) != 1 {
		return fmt.Errorf("%w: asset bytes do not match the trusted manifest", ErrInvalidAsset)
	}
	return nil
}

func validAssetID(value string) bool {
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

func validManifestPath(value string) bool {
	if value == "" || path.IsAbs(value) || strings.Contains(value, "\\") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
