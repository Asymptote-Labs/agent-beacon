package mcpconnect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testBeacon = "/opt/beacon/bin/beacon"

func localFileOptions(home string) LocalOptions {
	return LocalOptions{
		Home: home, Command: testBeacon,
		LookPath: func(string) (string, error) { return "", errors.New("not on PATH") },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no CLI may run in file-writer tests")
		},
	}
}

func localTargets() []Target {
	var out []Target
	for _, t := range Targets() {
		if t.LocalSupported() {
			out = append(out, t)
		}
	}
	return out
}

func TestLocalSupportedOnEveryAutomaticTarget(t *testing.T) {
	for _, target := range Targets() {
		if target.Automatic != target.LocalSupported() {
			t.Errorf("%s: automatic=%t local=%t", target.Name, target.Automatic, target.LocalSupported())
		}
	}
}

func TestInstallLocalCreatesEachConfigAndUninstallRemovesIt(t *testing.T) {
	home := isolate(t)
	opts := localFileOptions(home)
	items, err := InstallLocal(context.Background(), opts, localTargets())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(localTargets()) {
		t.Fatalf("items = %d, want %d", len(items), len(localTargets()))
	}
	for _, it := range items {
		if it.Err != nil || it.Action != ActionAdd {
			t.Fatalf("%s: action=%s err=%v", it.Target.Name, it.Action, it.Err)
		}
		st, err := inspectNamed(it.Target, it.Path, LocalServerName)
		if err != nil {
			t.Fatal(err)
		}
		if !st.exists || !sameLocalCommand(it.Target, st.entry, testBeacon) {
			t.Fatalf("%s: entry = %#v", it.Target.Name, st.entry)
		}
		// Beacon Cloud MCP's entry is a different name and stays unconfigured.
		if cloud, _ := inspect(it.Target, it.Path); cloud.exists {
			t.Fatalf("%s: local install wrote %s", it.Target.Name, ServerName)
		}
	}
	codex, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "[mcp_servers.beacon]\ncommand = \"/opt/beacon/bin/beacon\"\nargs = [\"mcp\", \"serve\"]\n"; string(codex) != want {
		t.Fatalf("codex config:\n%s\nwant:\n%s", codex, want)
	}

	// A second run finds every entry in place and writes nothing.
	again, err := InstallLocal(context.Background(), opts, localTargets())
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range again {
		if it.Action != ActionPresent {
			t.Fatalf("%s: second run action = %s", it.Target.Name, it.Action)
		}
	}

	removed, err := UninstallLocal(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != len(localTargets()) {
		t.Fatalf("removed %d items, want %d", len(removed), len(localTargets()))
	}
	for _, it := range removed {
		if it.Err != nil || it.Action != ActionRemove {
			t.Fatalf("%s: action=%s err=%v", it.Target.Name, it.Action, it.Err)
		}
		if _, err := os.Stat(it.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: %s should be gone, Beacon created it (err=%v)", it.Target.Name, it.Path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, LocalManifestPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty local manifest should be removed (err=%v)", err)
	}
}

func TestInstallLocalKeepsExistingContentAndEntries(t *testing.T) {
	home := isolate(t)
	cursor := mustTarget(t, "cursor")
	path := targetPath(t, cursor, home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{
  // my servers
  "mcpServers": {
    "other": {"command": "other"}
  }
}
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := InstallLocal(context.Background(), localFileOptions(home), []Target{cursor})
	if err != nil || len(items) != 1 || items[0].Err != nil {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "// my servers") || !strings.Contains(string(data), `"other": {"command": "other"}`) {
		t.Fatalf("existing content not preserved:\n%s", data)
	}
	if _, err := UninstallLocal(context.Background(), localFileOptions(home)); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != existing {
		t.Fatalf("uninstall did not restore the original content:\n%s", after)
	}
}

func TestInstallLocalNeverReplacesAnEntryNamedBeacon(t *testing.T) {
	home := isolate(t)
	gemini := mustTarget(t, "gemini_cli")
	path := targetPath(t, gemini, home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{"mcpServers": {"beacon": {"command": "/somewhere/else/beacon", "args": ["mcp", "serve"]}}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := InstallLocal(context.Background(), localFileOptions(home), []Target{gemini})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Action != ActionPresent {
		t.Fatalf("action = %s, want present", items[0].Action)
	}
	if data, _ := os.ReadFile(path); string(data) != existing {
		t.Fatalf("existing entry was changed:\n%s", data)
	}
	// Nothing was recorded, so uninstall leaves it alone too.
	removed, err := UninstallLocal(context.Background(), localFileOptions(home))
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
}

func TestUninstallLocalLeavesAnEntryChangedAfterInstall(t *testing.T) {
	home := isolate(t)
	gemini := mustTarget(t, "gemini_cli")
	if _, err := InstallLocal(context.Background(), localFileOptions(home), []Target{gemini}); err != nil {
		t.Fatal(err)
	}
	path := targetPath(t, gemini, home)
	changed := `{"mcpServers": {"beacon": {"command": "/mine/beacon", "args": ["mcp", "serve"]}}}`
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := UninstallLocal(context.Background(), localFileOptions(home))
	if err != nil || len(removed) != 1 || removed[0].Action != ActionAbsent {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
	if data, _ := os.ReadFile(path); string(data) != changed {
		t.Fatalf("changed entry was touched:\n%s", data)
	}
}

func TestInstallLocalUsesClaudeCLIWhenPresent(t *testing.T) {
	home := isolate(t)
	claude := mustTarget(t, "claude_code")
	path := targetPath(t, claude, home)
	var calls [][]string
	opts := localFileOptions(home)
	opts.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	opts.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		switch args[1] {
		case "add":
			// What `claude mcp add --scope user beacon -- <cmd> mcp serve` writes.
			return nil, os.WriteFile(path, []byte(`{"mcpServers":{"beacon":{"type":"stdio","command":"`+testBeacon+`","args":["mcp","serve"],"env":{}}}}`), 0o600)
		case "remove":
			return nil, os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o600)
		}
		return nil, errors.New("unexpected")
	}
	items, err := InstallLocal(context.Background(), opts, []Target{claude})
	if err != nil || items[0].Err != nil || items[0].Method != "claude CLI" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	want := "/usr/bin/claude mcp add --scope user beacon -- " + testBeacon + " mcp serve"
	if got := strings.Join(calls[0], " "); got != want {
		t.Fatalf("call = %q, want %q", got, want)
	}
	removed, err := UninstallLocal(context.Background(), opts)
	if err != nil || len(removed) != 1 || removed[0].Action != ActionRemove || removed[0].Err != nil {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
	if got := strings.Join(calls[1], " "); got != "/usr/bin/claude mcp remove beacon --scope user" {
		t.Fatalf("remove call = %q", got)
	}
}
