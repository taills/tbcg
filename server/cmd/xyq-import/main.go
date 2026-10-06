// Command xyq-import 是可选、显式启用的静态内容导入器：读取白名单来源与授权登记，
// 生成确定性的草稿批次与语义差异报告。它不执行旧脚本，不导入素材，不覆盖已有输出。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/taills/tbcg/server/internal/content/importer"
	"github.com/taills/tbcg/server/internal/content/schema"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "xyq-import:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("xyq-import", flag.ContinueOnError)
	source := flags.String("source", "", "来源根目录（包含 manifest.json 与白名单文件）")
	contracts := flags.String("contracts", "../contracts", "可信 contracts 目录")
	out := flags.String("out", "", "草稿批次输出文件（不存在或内容相同才写入）")
	reportPath := flags.String("report", "", "报告输出文件；为空时打印到标准输出")
	dryRun := flags.Bool("dry-run", true, "只生成报告，不写草稿批次")
	accept := flags.String("accept-approximate", "", "逗号分隔的、已人工确认的 approximate 内容 ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *source == "" {
		return errors.New("--source is required")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(*source, "manifest.json")) // #nosec G304 -- 操作者显式指定的来源目录。
	if err != nil {
		return err
	}
	var manifest importer.Manifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	registry, err := schema.NewRegistry(os.DirFS(*contracts))
	if err != nil {
		return err
	}
	accepted := map[string]bool{}
	for _, id := range strings.Split(*accept, ",") {
		if id = strings.TrimSpace(id); id != "" {
			accepted[id] = true
		}
	}
	report, bundle, err := importer.Run(os.DirFS(*source), manifest, registry, importer.Options{AcceptApproximate: accepted})
	if err != nil {
		return err
	}
	encodedReport, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if *reportPath == "" {
		fmt.Println(string(encodedReport))
	} else if err := writeOnce(*reportPath, append(encodedReport, '\n')); err != nil {
		return err
	}
	if *dryRun {
		return nil
	}
	if *out == "" {
		return errors.New("--out is required when --dry-run=false")
	}
	encodedBundle, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	return writeOnce(*out, append(encodedBundle, '\n'))
}

// writeOnce 拒绝覆盖不同内容的既有文件；相同字节视为幂等重放。
func writeOnce(path string, data []byte) error {
	existing, err := os.ReadFile(path) // #nosec G304 -- 操作者显式指定的输出路径。
	if err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("refusing to overwrite %s with different content", path)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
