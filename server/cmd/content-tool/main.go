// Command content-tool 是本地创作辅助：校验内容目录、生成开发公开包与战斗 golden fixture。
// 它只读取仓库内受信任的 Schema 与内容目录，不访问数据库，也不产生可发布 Release。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	contentbuild "github.com/taills/tbcg/server/internal/content/build"
	"github.com/taills/tbcg/server/internal/content/catalog"
	"github.com/taills/tbcg/server/internal/content/mapdata"
	"github.com/taills/tbcg/server/internal/content/schema"
	"github.com/taills/tbcg/server/modules/battle/preview"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "content-tool:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: content-tool <validate|public-bundle|battle-golden> [flags]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	root := flags.String("root", "..", "project root containing contracts/ and games/")
	game := flags.String("game", "demo", "game id")
	out := flags.String("out", "", "output file")
	encounter := flags.String("encounter", "demo:encounter/slime-pack", "encounter for battle-golden")
	seed := flags.Uint64("seed", 20261007, "seed for battle-golden")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	contracts := os.DirFS(filepath.Join(*root, "contracts"))
	registry, err := schema.NewRegistry(contracts)
	if err != nil {
		return err
	}
	geometry, err := mapdata.NewValidator(contracts)
	if err != nil {
		return err
	}
	entries, err := catalog.ReadDir(os.DirFS(filepath.Join(*root, "games", *game, "content")))
	if err != nil {
		return err
	}
	content, issues := catalog.Load(*game, entries, registry, geometry)
	switch args[0] {
	case "validate":
		report := map[string]any{"entry_count": len(entries), "valid": len(issues) == 0, "issues": issues, "schema_fingerprint": registry.Fingerprint()}
		return write(*out, report)
	case "public-bundle":
		if len(issues) != 0 {
			return fmt.Errorf("content invalid: %s", issues[0].Error())
		}
		public, err := catalog.PublicProjection(registry, entries)
		if err != nil {
			return err
		}
		return write(*out, contentbuild.PublicSnapshot{FormatVersion: 1, Scope: "public", GameID: *game, ReleaseID: "dev-local", Entries: public})
	case "battle-golden":
		if len(issues) != 0 {
			return fmt.Errorf("content invalid: %s", issues[0].Error())
		}
		request, _ := json.Marshal(map[string]any{"encounter_id": *encounter, "seed": *seed,
			"party": []map[string]any{{"archetype_id": "demo:archetype/wanderer", "level": 2}}})
		result, err := preview.Runner{}.PreviewBattle(content, request)
		if err != nil {
			return err
		}
		return write(*out, result)
	}
	return fmt.Errorf("unknown subcommand %q", args[0])
}

func write(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if path == "" {
		_, err = os.Stdout.Write(encoded)
		return err
	}
	return os.WriteFile(path, encoded, 0o600) // #nosec G703 -- 本地创作工具，输出路径由操作者显式指定。
}
