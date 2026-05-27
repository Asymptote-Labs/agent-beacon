package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVSCodeUserSettingsPathByOS(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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
	t.Setenv("HOME", home)
	path := filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	existing := `{"editor.formatOnSave":true}`
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
