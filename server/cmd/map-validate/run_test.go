package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("TBCG_MAP_VALIDATE_CHILD") == "1" {
		var args []string
		if err := json.Unmarshal([]byte(os.Getenv("TBCG_MAP_VALIDATE_ARGS")), &args); err != nil {
			os.Exit(2)
		}
		os.Args = []string{os.Args[0]}
		os.Args = append(os.Args, args...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRunHelpSucceedsWithoutInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("help should succeed: %v", err)
	}
	if !strings.Contains(stderr.String(), "contracts") || !strings.Contains(stderr.String(), "input") {
		t.Fatal("help must describe both input options")
	}
}

func TestRunValidatesMapInput(t *testing.T) {
	contracts := filepath.Join("..", "..", "..", "contracts")
	input := filepath.Join("..", "..", "..", "tests", "fixtures", "maps", "training-ground.json")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--contracts", contracts, "--input", input}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v, stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "valid") {
		t.Fatalf("stdout = %q, want successful validation message", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestMainProcessExitCodes(t *testing.T) {
	contracts := filepath.Join("..", "..", "..", "contracts")
	validInput := filepath.Join("..", "..", "..", "tests", "fixtures", "maps", "training-ground.json")
	invalidInput := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalidInput, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		input    string
		wantCode int
	}{
		{name: "valid export", input: validInput, wantCode: 0},
		{name: "invalid export", input: invalidInput, wantCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, err := json.Marshal([]string{"--contracts", contracts, "--input", tc.input})
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=TestMainProcessExitCodes")
			command.Env = append(os.Environ(), "TBCG_MAP_VALIDATE_CHILD=1", "TBCG_MAP_VALIDATE_ARGS="+string(args))
			output, err := command.CombinedOutput()
			code := 0
			if err != nil {
				exitError, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatalf("child process error = %v, output = %s", err, output)
				}
				code = exitError.ExitCode()
			}
			if code != tc.wantCode {
				t.Fatalf("CLI exit code = %d, want %d; output = %s", code, tc.wantCode, output)
			}
		})
	}
}

func TestRunRejectsInvalidInputAndArguments(t *testing.T) {
	contracts := filepath.Join("..", "..", "..", "contracts")
	invalidInput := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalidInput, []byte(`{"not":"a map"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
	}{
		{name: "schema-invalid document", args: []string{"--contracts", contracts, "--input", invalidInput}},
		{name: "missing input", args: []string{"--contracts", contracts}},
		{name: "unknown argument", args: []string{"--contracts", contracts, "--input", invalidInput, "--unexpected"}},
		{name: "missing contract directory", args: []string{"--contracts", filepath.Join(t.TempDir(), "missing"), "--input", invalidInput}},
		{name: "missing input file", args: []string{"--contracts", contracts, "--input", filepath.Join(t.TempDir(), "missing.json")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run(tc.args, &stdout, &stderr); err == nil {
				t.Fatal("run() error = nil, want failure")
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty on failure", stdout.String())
			}
		})
	}
}
