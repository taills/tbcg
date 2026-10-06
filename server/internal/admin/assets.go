package admin

import (
	"io/fs"
	"path"
	"regexp"

	contentassets "example.com/tbcg/server/internal/content/assets"
)

var gameAssetPathID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func assetsForGame(gameRoot fs.FS, gameID string) fs.FS {
	if gameRoot == nil || !gameAssetPathID.MatchString(gameID) {
		return nil
	}
	assets, err := fs.Sub(gameRoot, path.Join(gameID, "assets"))
	if err != nil {
		return nil
	}
	return assets
}

func validateIconReference(iconID string, assetFS fs.FS) []fieldIssue {
	failure := []fieldIssue{{Pointer: "/public/icon_id", Code: "invalid_reference", Message: "图标不存在、哈希不匹配或缺少来源许可记录"}}
	if _, err := contentassets.ReadIcon(assetFS, iconID); err != nil {
		return failure
	}
	return nil
}
