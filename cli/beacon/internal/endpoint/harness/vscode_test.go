package harness

import (
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVSCodeUserSettingsPathByOS(t *testing.T) {
	home := t.TempDir()
	isolateUserConfig(t, home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))

	tests := []struct {
		goos string
		want string
	}{
		{"darwin", filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json")},
		{"linux", filepath.Join(home, "xdg", "Code", "User", "settings.json")},
		{"windows", filepath.Join(home, "AppData", "Roaming", "Code", "User", "settings.json")},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			got, err := vscodeUserSettingsPath(tt.goos)
			if err != nil {
				t.Fatalf("vscodeUserSettingsPath returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("settings path = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigureVSCodePreservesSettingsAndDisablesContentCaptureByDefault(t *testing.T) {
	home := t.TempDir()
	isolateUserConfig(t, home)
	// VS Code stores user settings per platform; assert the platform-correct location rather
	// than the macOS one, which silently passed only because tests never ran on Linux.
	path := vscodeSettingsPathForTest(home)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	existing := "{\n    \"editor.formatOnSave\": true\n}\n"
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := ConfigureVSCode(VSCodeConfigOptions{Endpoint: "http://127.0.0.1:54318"})
	if err != nil {
		t.Fatalf("ConfigureVSCode returned error: %v", err)
	}
	if got != path {
		t.Fatalf("configured path = %q, want %q", got, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`"editor.formatOnSave": true`,
		`"github.copilot.chat.otel.enabled": true`,
		`"github.copilot.chat.otel.exporterType": "otlp-http"`,
		`"github.copilot.chat.otel.otlpEndpoint": "http://127.0.0.1:54318"`,
		`"github.copilot.chat.otel.captureContent": false`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings missing %q:\n%s", want, text)
		}
	}
	backups, err := filepath.Glob(path + ".beacon.*.bak")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %#v, want one timestamped backup", backups)
	}
}

// Setup sets Beacon's keys in place: a hand-edited settings.json keeps its comments, trailing
// commas, order and indentation, and a value already there is replaced where it stands.
func TestConfigureVSCodeKeepsACommentedSettingsFile(t *testing.T) {
	home := t.TempDir()
	isolateUserConfig(t, home)
	path := vscodeSettingsPathForTest(home)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	existing := `{
    // Editor
    "editor.fontSize": 13,
    /* Copilot: telemetry off until Beacon is set up */
    "github.copilot.chat.otel.enabled": false,
    "files.exclude": {
        "**/.git": true, // keep
    },
}
`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureVSCode(VSCodeConfigOptions{Endpoint: "http://127.0.0.1:54318"}); err != nil {
		t.Fatalf("ConfigureVSCode returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
    // Editor
    "editor.fontSize": 13,
    /* Copilot: telemetry off until Beacon is set up */
    "github.copilot.chat.otel.enabled": true,
    "files.exclude": {
        "**/.git": true, // keep
    },
    "github.copilot.chat.otel.exporterType": "otlp-http",
    "github.copilot.chat.otel.otlpEndpoint": "http://127.0.0.1:54318",
    "github.copilot.chat.otel.captureContent": false,
    "github.copilot.chat.otel.maxAttributeSizeChars": 8192,
}
`
	if string(data) != want {
		t.Fatalf("settings.json =\n%s\nwant\n%s", data, want)
	}
	if status, msg := VSCodeOTelStatus(path, "http://127.0.0.1:54318"); status != TelemetryEnabled {
		t.Fatalf("status after setup = %s msg=%s, want enabled", status, msg)
	}
}

func TestVSCodeOTelStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"github.copilot.chat.otel.enabled":true,"github.copilot.chat.otel.otlpEndpoint":"http://127.0.0.1:4318"}`), 0600); err != nil {
		t.Fatal(err)
	}
	status, msg := VSCodeOTelStatus(path, "http://127.0.0.1:4318")
	if status != TelemetryEnabled {
		t.Fatalf("status = %s msg=%s, want enabled", status, msg)
	}

	if err := os.WriteFile(path, []byte(`{"github.copilot.chat.otel.enabled":true,"github.copilot.chat.otel.otlpEndpoint":"https://example.com"}`), 0600); err != nil {
		t.Fatal(err)
	}
	status, _ = VSCodeOTelStatus(path, "http://127.0.0.1:4318")
	if status != TelemetryMisconfigured {
		t.Fatalf("remote endpoint status = %s, want misconfigured", status)
	}
}

// VS Code's settings.json is JSONC: comments and trailing commas are legal there, and a
// hand-edited one usually has some.
func TestVSCodeOTelStatusReadsJSONC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	body := `{
  // Copilot telemetry goes to Beacon.
  "github.copilot.chat.otel.enabled": true,
  /* local receiver, see "beacon endpoint status" */
  "github.copilot.chat.otel.otlpEndpoint": "http://127.0.0.1:4318",
  "editor.rulers": [80, 120,],
  "files.exclude": {"**/*.min.js": true,},
  "url.note": "http://example.com/a//b, not a comment",
}
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if status, msg := VSCodeOTelStatus(path, "http://127.0.0.1:4318"); status != TelemetryEnabled {
		t.Fatalf("status = %s msg=%s, want enabled", status, msg)
	}
	// A commented-out key is not a setting, and an empty file is VS Code's empty settings.
	for _, doc := range []string{"{\n  // \"github.copilot.chat.otel.enabled\": true,\n}\n", ""} {
		if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
		if status, msg := VSCodeOTelStatus(path, "http://127.0.0.1:4318"); status != TelemetryDisabled {
			t.Fatalf("%q: status = %s msg=%s, want disabled", doc, status, msg)
		}
	}
}

// vscodeSettingsPathForTest mirrors vscodeUserDataDirsForOS so these tests exercise the real
// per-platform layout.
// isolateUserConfig points every user-config lookup at a temporary home.
//
// HOME alone is not enough on Linux: VS Code settings live under XDG_CONFIG_HOME when it is set, and
// GitHub runners set it. A test that only overrode HOME therefore read and wrote the *real* user's
// settings path, which passed locally and failed in CI -- and would have been a test quietly editing
// a developer's own VS Code configuration.
func isolateUserConfig(t *testing.T, home string) {
	t.Helper()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func vscodeSettingsPathForTest(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json")
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "Code", "User", "settings.json")
	default:
		return filepath.Join(home, ".config", "Code", "User", "settings.json")
	}
}
