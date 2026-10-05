package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"path"
	"strings"
)

type assetManifest struct {
	SchemaVersion int                `json:"schema_version"`
	Assets        []assetManifestRow `json:"assets"`
}

type assetManifestRow struct {
	AssetID string `json:"asset_id"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Source  string `json:"source"`
	License string `json:"license"`
}

func validateIconReference(iconID string, assets fs.FS) []fieldIssue {
	failure := []fieldIssue{{Pointer: "/public/icon_id", Code: "invalid_reference", Message: "图标不存在、哈希不匹配或缺少来源许可记录"}}
	if assets == nil || strings.TrimSpace(iconID) == "" {
		return failure
	}
	manifestData, err := fs.ReadFile(assets, "manifest.json")
	if err != nil {
		return failure
	}
	var manifest assetManifest
	decoder := json.NewDecoder(strings.NewReader(string(manifestData)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || manifest.SchemaVersion != 1 {
		return failure
	}
	for _, asset := range manifest.Assets {
		if asset.AssetID != iconID {
			continue
		}
		if strings.TrimSpace(asset.Source) == "" || strings.TrimSpace(asset.License) == "" ||
			!validManifestPath(asset.Path) || len(asset.SHA256) != sha256.Size*2 {
			return failure
		}
		declaredHash, err := hex.DecodeString(asset.SHA256)
		if err != nil {
			return failure
		}
		data, err := fs.ReadFile(assets, asset.Path)
		if err != nil {
			return failure
		}
		actualHash := sha256.Sum256(data)
		if subtle.ConstantTimeCompare(actualHash[:], declaredHash) != 1 {
			return failure
		}
		return nil
	}
	return failure
}

func validManifestPath(value string) bool {
	if value == "" || path.IsAbs(value) || strings.Contains(value, "\\") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
