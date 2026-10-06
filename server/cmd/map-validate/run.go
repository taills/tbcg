package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/taills/tbcg/server/internal/content/mapdata"
)

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("map-validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	contractsDir := flags.String("contracts", "", "directory containing content/map_geometry.schema.json")
	inputPath := flags.String("input", "", "exported map geometry JSON file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *contractsDir == "" || *inputPath == "" {
		return errors.New("both --contracts and --input are required")
	}
	validator, err := mapdata.NewValidator(os.DirFS(*contractsDir))
	if err != nil {
		return fmt.Errorf("initialize map validator: %w", err)
	}
	data, err := os.ReadFile(*inputPath)
	if err != nil {
		return fmt.Errorf("read map input %q: %w", *inputPath, err)
	}
	if _, err := validator.Decode(data); err != nil {
		return fmt.Errorf("validate map input %q: %w", *inputPath, err)
	}
	if _, err := fmt.Fprintln(stdout, "map geometry valid"); err != nil {
		return fmt.Errorf("write validation result: %w", err)
	}
	return nil
}
