package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManagerLabelAndPlistPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	userManager := Manager{UserMode: true}
	if got := userManager.Label(); got != UserLabel {
		t.Fatalf("user label = %q, want %q", got, UserLabel)
	}
	userPath, err := userManager.PlistPath()
	if err != nil {
		t.Fatalf("user PlistPath returned error: %v", err)
	}
	if want := filepath.Join(home, "Library", "LaunchAgents", UserLabel+".plist"); userPath != want {
		t.Fatalf("user plist path = %q, want %q", userPath, want)
	}

	systemManager := Manager{}
	if got := systemManager.Label(); got != SystemLabel {
		t.Fatalf("system label = %q, want %q", got, SystemLabel)
	}
	systemPath, err := systemManager.PlistPath()
	if err != nil {
		t.Fatalf("system PlistPath returned error: %v", err)
	}
	if want := filepath.Join("/Library/LaunchDaemons", SystemLabel+".plist"); systemPath != want {
		t.Fatalf("system plist path = %q, want %q", systemPath, want)
	}
}

func TestPlistContainsLaunchdContract(t *testing.T) {
	content := plist(UserLabel, "/tmp/otelcol", "/tmp/otelcol.yaml")

	for _, want := range []string{
		"<string>" + UserLabel + "</string>",
		"<string>/tmp/otelcol</string>",
		"<string>--config</string>",
		"<string>/tmp/otelcol.yaml</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<string>/tmp/" + UserLabel + ".out</string>",
		"<string>/tmp/" + UserLabel + ".err</string>",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("plist missing %q:\n%s", want, content)
		}
	}
}

func TestWritePlistUserMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manager := Manager{UserMode: true}

	path, err := manager.WritePlist("/tmp/otelcol", "/tmp/otelcol.yaml")
	if runtime.GOOS != "darwin" {
		if err == nil || !strings.Contains(err.Error(), "supported only on macOS") {
			t.Fatalf("WritePlist non-darwin error = %v, want macOS support error", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("WritePlist returned error: %v", err)
	}
	if got, want := path, filepath.Join(home, "Library", "LaunchAgents", UserLabel+".plist"); got != want {
		t.Fatalf("plist path = %q, want %q", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	if !strings.Contains(string(data), "<string>/tmp/otelcol</string>") {
		t.Fatalf("plist content missing program: %s", string(data))
	}
}

func TestStatusNonDarwinDocumentsUnsupportedMessage(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-darwin status contract")
	}
	status := (Manager{UserMode: true}).Status()
	if status.Label != UserLabel {
		t.Fatalf("Label = %q, want %q", status.Label, UserLabel)
	}
	if status.Loaded || status.Running {
		t.Fatalf("non-darwin status should not be loaded/running: %#v", status)
	}
	if !strings.Contains(status.Message, "available only on macOS") {
		t.Fatalf("unexpected status message: %q", status.Message)
	}
}
