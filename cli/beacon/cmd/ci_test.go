package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestCICommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"ci"},
		{"ci", "exec"},
		{"ci", "validate"},
	} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil {
			t.Fatalf("command %v not registered", path)
		}
	}
	for _, name := range []string{"harness", "log-path", "min-events", "json"} {
		if ciExecCmd.Flags().Lookup(name) == nil {
			t.Fatalf("ci exec missing --%s", name)
		}
		if ciValidateCmd.Flags().Lookup(name) == nil {
			t.Fatalf("ci validate missing --%s", name)
		}
	}
	for _, name := range []string{"content-retention", "keep-artifacts", "upload"} {
		if ciExecCmd.Flags().Lookup(name) == nil {
			t.Fatalf("ci exec missing --%s", name)
		}
	}
	if ciExecCmd.Flags().Lookup("require-harness") != nil || ciValidateCmd.Flags().Lookup("require-harness") != nil {
		t.Fatal("CI commands should not expose --require-harness")
	}
	for _, name := range []string{"base-dir", "work-dir", "collector", "otlp-grpc-port", "otlp-http-port"} {
		flag := ciExecCmd.Flags().Lookup(name)
		if flag == nil {
			t.Fatalf("ci exec missing advanced --%s", name)
		}
		if !flag.Hidden {
			t.Fatalf("ci exec --%s should be hidden", name)
		}
	}
}

func TestCIActionExposesUploadInput(t *testing.T) {
	data, err := os.ReadFile("../../../action.yml")
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}
	text := string(data)
	for _, want := range []string{"  upload:", "INPUT_UPLOAD", "--upload"} {
		if !strings.Contains(text, want) {
			t.Fatalf("action.yml missing %q", want)
		}
	}
}

func TestCIHarnessDefaultIsClaudeOnly(t *testing.T) {
	flag := ciExecCmd.Flags().Lookup("harness")
	if flag == nil {
		t.Fatal("ci exec missing --harness")
	}
	if flag.DefValue != "claude" {
		t.Fatalf("ci exec --harness default = %q, want claude", flag.DefValue)
	}
}
