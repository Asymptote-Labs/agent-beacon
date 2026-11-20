package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/mcpmanaged"
)

func newMCPManagedFixture(t *testing.T, privileged bool) mcpmanaged.Paths {
	t.Helper()
	dir := t.TempDir()
	paths := mcpmanaged.Paths{
		ClaudeDropIn: filepath.Join(dir, "ClaudeCode", "managed-settings.d", mcpmanaged.ClaudeDropInName),
		CodexConfig:  filepath.Join(dir, "codex", "config.toml"),
	}
	prevPaths, prevPriv, prevStdin := mcpManagedPaths, mcpManagedPrivileged, mcpManagedStdin
	t.Cleanup(func() { mcpManagedPaths, mcpManagedPrivileged, mcpManagedStdin = prevPaths, prevPriv, prevStdin })
	mcpManagedPaths = func() (mcpmanaged.Paths, error) { return paths, nil }
	mcpManagedPrivileged = func() bool { return privileged }
	mcpManagedStdin = strings.NewReader("")
	return paths
}

const mcpManagedTestList = `{"servers": {"linear": {"url": "https://mcp.linear.app/mcp"}}}`

func TestMCPManagedApplyFromStdinThenStatusAndRemove(t *testing.T) {
	paths := newMCPManagedFixture(t, true)
	mcpManagedStdin = strings.NewReader(mcpManagedTestList)
	stdout, _, err := runMCP(t, "managed", "apply", "--file", "-")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, stdout)
	}
	for _, want := range []string{"Claude Code", "Codex CLI", "write", "linear"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("apply output lacks %q:\n%s", want, stdout)
		}
	}
	for _, path := range []string{paths.ClaudeDropIn, paths.CodexConfig} {
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), "https://mcp.linear.app/mcp") {
			t.Fatalf("%s: %v %q", path, err, data)
		}
	}

	stdout, _, err = runMCP(t, "managed", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Runtimes []mcpmanaged.RuntimeStatus `json:"runtimes"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	if len(status.Runtimes) != 2 {
		t.Fatalf("status = %+v", status)
	}
	for _, rt := range status.Runtimes {
		want := "linear"
		if rt.Runtime == mcpmanaged.RuntimeCodex {
			want = "org-linear"
		}
		if len(rt.Servers) != 1 || rt.Servers[0].Name != want {
			t.Errorf("%s servers = %+v", rt.Runtime, rt.Servers)
		}
	}

	if _, _, err := runMCP(t, "managed", "remove", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.ClaudeDropIn); !os.IsNotExist(err) {
		t.Fatal("remove --harness claude left the Claude drop-in")
	}
	if data, _ := os.ReadFile(paths.CodexConfig); !strings.Contains(string(data), "linear") {
		t.Fatal("remove --harness claude touched Codex")
	}
}

func TestMCPManagedRefusesWithoutPrivilegesButDryRunWorks(t *testing.T) {
	paths := newMCPManagedFixture(t, false)
	file := filepath.Join(t.TempDir(), "servers.json")
	if err := os.WriteFile(file, []byte(mcpManagedTestList), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runMCP(t, "managed", "apply", "--file", file); err == nil || !strings.Contains(err.Error(), "machine-wide") {
		t.Fatalf("apply without privileges: %v", err)
	}
	if _, _, err := runMCP(t, "managed", "remove"); err == nil {
		t.Fatal("remove without privileges succeeded")
	}
	stdout, _, err := runMCP(t, "managed", "apply", "--file", file, "--dry-run")
	if err != nil || !strings.Contains(stdout, "Dry run") {
		t.Fatalf("dry run: %v\n%s", err, stdout)
	}
	for _, path := range []string{paths.ClaudeDropIn, paths.CodexConfig} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s written without privileges", path)
		}
	}
	if _, _, err := runMCP(t, "managed", "status"); err != nil {
		t.Fatalf("status needs no privileges: %v", err)
	}
}

func TestMCPManagedApplyRejectsBadInput(t *testing.T) {
	newMCPManagedFixture(t, true)
	if _, _, err := runMCP(t, "managed", "apply"); err == nil || !strings.Contains(err.Error(), "--file") {
		t.Fatalf("no --file: %v", err)
	}
	mcpManagedStdin = strings.NewReader(`{"servers": {"x": {"url": "http://insecure.example"}}}`)
	if _, _, err := runMCP(t, "managed", "apply", "--file", "-"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("http url: %v", err)
	}
	mcpManagedStdin = strings.NewReader(mcpManagedTestList)
	if _, _, err := runMCP(t, "managed", "apply", "--file", "-", "--harness", "cursor"); err == nil {
		t.Fatal("--harness cursor accepted")
	}
	mcpManagedStdin = io.MultiReader(strings.NewReader(`{"servers": {}, "pad": "`), strings.NewReader(strings.Repeat("a", 1<<20)), strings.NewReader(`"}`))
	if _, _, err := runMCP(t, "managed", "apply", "--file", "-"); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("oversized list: %v", err)
	}
}

func TestMCPManagedFailsWhenARuntimeCannotBeUpdated(t *testing.T) {
	paths := newMCPManagedFixture(t, true)
	if err := os.MkdirAll(filepath.Dir(paths.CodexConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.CodexConfig, []byte("not = = toml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mcpManagedStdin = strings.NewReader(mcpManagedTestList)
	_, _, err := runMCP(t, "managed", "apply", "--file", "-")
	if err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(paths.ClaudeDropIn); err != nil {
		t.Fatal("a Codex failure stopped the Claude Code write")
	}
}
