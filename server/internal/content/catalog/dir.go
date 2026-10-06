package catalog

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"example.com/tbcg/server/internal/content/schema"
)

const maxDefinitionBytes = 1 << 20

// ReadDir 读取 `<type>/<slug>.json` 布局的本地内容目录（开发与测试 fixture）。
func ReadDir(fsys fs.FS) ([]Entry, error) {
	var entries []Entry
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("content file %s must not be a symlink", name)
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		if len(data) > maxDefinitionBytes {
			return fmt.Errorf("content file %s exceeds 1 MiB", name)
		}
		var identity identity
		if err := json.Unmarshal(data, &identity); err != nil {
			return fmt.Errorf("decode %s: %w", name, err)
		}
		contentType, err := schema.ContentTypeOf(identity.ContentID)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if path.Dir(name) != contentType {
			return fmt.Errorf("content file %s must live under %s/", name, contentType)
		}
		entries = append(entries, Entry{ContentID: identity.ContentID, ContentType: contentType, Revision: identity.Revision, Definition: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ContentID < entries[j].ContentID })
	return entries, nil
}
