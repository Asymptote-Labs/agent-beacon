package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutopilotStatus(t *testing.T) {
	beacon := `'/opt/beacon/beacon-hooks' --platform autopilot --log '/tmp/runtime.jsonl' stop`
	for name, tc := range map[string]struct {
		content string
		want    TelemetryStatus
	}{
		"installed":         {`{"hooks":{"Stop":[{"id":"beacon-stop","command":"` + jsonEscape(beacon) + `"}]}}`, TelemetryEnabled},
		"disabled by user":  {`{"hooks":{"Stop":[{"id":"beacon-stop","command":"` + jsonEscape(beacon) + `","enabled":false}]}}`, TelemetryDisabled},
		"other hooks only":  {`{"hooks":{"Stop":[{"command":"printf bell"}]}}`, TelemetryDisabled},
		"claude-mode hooks": {`{"hooks":{"Stop":[{"id":"beacon-stop","command":"beacon-hooks --platform claude stop"}]}}`, TelemetryDisabled},
		"invalid":           {`{"hooks":`, TelemetryMisconfigured},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, msg := autopilotStatus(path); got != tc.want {
				t.Fatalf("autopilotStatus = %q (%s), want %q", got, msg, tc.want)
			}
		})
	}
	if got, _ := autopilotStatus(filepath.Join(t.TempDir(), "missing.json")); got != TelemetryMissing {
		t.Fatalf("missing file = %q, want missing", got)
	}
}

func TestDiscoverAutopilotReadsTheInstallersPath(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("PATH", t.TempDir())
	h := DiscoverAutopilot()
	if want := filepath.Join(xdg, "autopilot", "settings.json"); h.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", h.ConfigPath, want)
	}
	if h.Detected {
		t.Fatal("Detected with no binary and no config directory")
	}
	if err := os.MkdirAll(filepath.Join(xdg, "autopilot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if h := DiscoverAutopilot(); !h.Detected || h.Name != "autopilot" {
		t.Fatalf("DiscoverAutopilot = %+v, want detected from its config directory", h)
	}
}

func jsonEscape(s string) string {
	out := ""
	for _, r := range s {
		if r == '"' || r == '\\' {
			out += `\`
		}
		out += string(r)
	}
	return out
}
