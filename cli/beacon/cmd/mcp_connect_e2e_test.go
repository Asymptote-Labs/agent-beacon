package cmd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests run the harnesses' own CLIs against the configs `beacon mcp connect` writes, in a
// temporary home, and skip any CLI that is not installed. They are how the entry shapes were
// verified: each harness must list beacon-managed at the connected URL, and where the CLI connects
// while listing, a token-env config must make it send the token from the environment.

func realCLI(t *testing.T, name string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("real-CLI tests are skipped in -short mode")
	}
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed", name)
	}
	return path
}

func runCLI(t *testing.T, dir, bin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// realCLIFixture is an MCP fixture whose harness CLIs run for real.
func realCLIFixture(t *testing.T, detected string) *mcpFixture {
	fx := newMCPFixture(t, detected)
	mcpLookPath, mcpRunCLI = exec.LookPath, nil
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("no_proxy", "127.0.0.1,localhost")
	return fx
}

func connectReal(t *testing.T, fx *mcpFixture, harness string, extra ...string) {
	t.Helper()
	stdout, stderr, err := runMCP(t, append([]string{"connect", "--url", fx.url, "--yes", "--harness", harness}, extra...)...)
	if err != nil || !strings.Contains(stdout, "✓") {
		t.Fatalf("connect %s: %v\n%s%s", harness, err, stdout, stderr)
	}
}

const e2eToken = "bcn_mcp_e2e_token_from_the_environment"

func TestMCPConnectEndToEndClaudeCode(t *testing.T) {
	bin := realCLI(t, "claude")
	fx := realCLIFixture(t, "claude_code")
	connectReal(t, fx, "claude")
	out, err := runCLI(t, fx.home, bin, "mcp", "get", "beacon-managed")
	if !strings.Contains(out, "URL: "+fx.url) || !strings.Contains(out, "User config") {
		t.Fatalf("claude mcp get: %v\n%s", err, out)
	}
	if _, _, err := runMCP(t, "disconnect", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if out, _ := runCLI(t, fx.home, bin, "mcp", "get", "beacon-managed"); strings.Contains(out, fx.url) {
		t.Fatalf("still configured after disconnect:\n%s", out)
	}

	t.Setenv("BEACON_MCP_TOKEN", e2eToken)
	connectReal(t, fx, "claude", "--token-env", "BEACON_MCP_TOKEN")
	_, _ = runCLI(t, fx.home, bin, "mcp", "get", "beacon-managed") // connects to the server
	if !containsString(fx.auth.all(), "Bearer "+e2eToken) {
		t.Fatalf("Claude Code did not send the token from BEACON_MCP_TOKEN; saw %q", fx.auth.all())
	}
}

func TestMCPConnectEndToEndCodex(t *testing.T) {
	bin := realCLI(t, "codex")
	fx := realCLIFixture(t, "codex_cli")
	// Codex refuses to create helper binaries under a temporary directory unless CODEX_HOME is
	// set explicitly; it still works, but say where the config lives.
	t.Setenv("CODEX_HOME", filepath.Join(fx.home, ".codex"))
	connectReal(t, fx, "codex", "--token-env", "BEACON_MCP_TOKEN")
	out, err := runCLI(t, fx.home, bin, "mcp", "list", "--json")
	if err != nil {
		t.Fatalf("codex mcp list: %v\n%s", err, out)
	}
	var servers []struct {
		Name      string `json:"name"`
		Transport struct {
			URL               string  `json:"url"`
			BearerTokenEnvVar *string `json:"bearer_token_env_var"`
		} `json:"transport"`
	}
	if err := json.Unmarshal([]byte(out[strings.Index(out, "["):]), &servers); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	found := false
	for _, s := range servers {
		if s.Name == "beacon-managed" && s.Transport.URL == fx.url && s.Transport.BearerTokenEnvVar != nil && *s.Transport.BearerTokenEnvVar == "BEACON_MCP_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("codex does not list beacon-managed at %s with the token variable:\n%s", fx.url, out)
	}
	if _, _, err := runMCP(t, "disconnect", "--harness", "codex"); err != nil {
		t.Fatal(err)
	}
	if out, _ := runCLI(t, fx.home, bin, "mcp", "list", "--json"); strings.Contains(out, "beacon-managed") {
		t.Fatalf("still listed after disconnect:\n%s", out)
	}
}

func TestMCPConnectEndToEndGemini(t *testing.T) {
	bin := realCLI(t, "gemini")
	fx := realCLIFixture(t, "gemini_cli")
	t.Setenv("BEACON_MCP_TOKEN", e2eToken)
	connectReal(t, fx, "gemini", "--token-env", "BEACON_MCP_TOKEN")
	// Gemini CLI connects to MCP servers only in a trusted folder.
	trusted, _ := json.Marshal(map[string]string{fx.home: "TRUST_FOLDER"})
	if err := os.WriteFile(filepath.Join(fx.home, ".gemini", "trustedFolders.json"), trusted, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, fx.home, bin, "mcp", "list")
	if !strings.Contains(out, "beacon-managed: "+fx.url+" (http)") {
		t.Fatalf("gemini mcp list: %v\n%s", err, out)
	}
	if !containsString(fx.auth.all(), "Bearer "+e2eToken) {
		t.Fatalf("Gemini CLI did not send the token from BEACON_MCP_TOKEN; saw %q", fx.auth.all())
	}
	if _, _, err := runMCP(t, "disconnect", "--harness", "gemini"); err != nil {
		t.Fatal(err)
	}
	if out, _ := runCLI(t, fx.home, bin, "mcp", "list"); strings.Contains(out, "beacon-managed") {
		t.Fatalf("still listed after disconnect:\n%s", out)
	}
}

func TestMCPConnectEndToEndOpenCode(t *testing.T) {
	bin := realCLI(t, "opencode")
	fx := realCLIFixture(t, "opencode")
	connectReal(t, fx, "opencode")
	out, err := runCLI(t, fx.home, bin, "mcp", "list")
	if !strings.Contains(out, "beacon-managed") || !strings.Contains(out, fx.url) {
		t.Fatalf("opencode mcp list: %v\n%s", err, out)
	}
	if _, _, err := runMCP(t, "disconnect", "--harness", "opencode"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BEACON_MCP_TOKEN", e2eToken)
	connectReal(t, fx, "opencode", "--token-env", "BEACON_MCP_TOKEN")
	_, _ = runCLI(t, fx.home, bin, "mcp", "list")
	if !containsString(fx.auth.all(), "Bearer "+e2eToken) {
		t.Fatalf("OpenCode did not send the token from BEACON_MCP_TOKEN; saw %q", fx.auth.all())
	}
}

// Copilot CLI is a manual target: connect prints the command. Running that command, with a
// placeholder token, must produce a server Copilot lists at the URL.
func TestMCPConnectEndToEndCopilotManualStep(t *testing.T) {
	bin := realCLI(t, "copilot")
	fx := realCLIFixture(t, "copilot_cli")
	stdout, _, err := runMCP(t, "connect", "--url", fx.url, "--yes", "--harness", "copilot")
	if err != nil {
		t.Fatal(err)
	}
	want := "copilot mcp add --transport http beacon-managed " + fx.url + ` --header "Authorization: Bearer <token>"`
	if !strings.Contains(stdout, want) {
		t.Fatalf("output lacks the manual step:\n%s", stdout)
	}
	out, err := runCLI(t, fx.home, bin, "mcp", "add", "--transport", "http", "beacon-managed", fx.url, "--header", "Authorization: Bearer placeholder")
	if err != nil {
		t.Fatalf("the printed copilot command failed: %v\n%s", err, out)
	}
	if out, _ := runCLI(t, fx.home, bin, "mcp", "list"); !strings.Contains(out, "beacon-managed") {
		t.Fatalf("copilot mcp list:\n%s", out)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
