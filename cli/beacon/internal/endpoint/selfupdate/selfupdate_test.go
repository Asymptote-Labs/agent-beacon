package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

func TestClassifyInstall(t *testing.T) {
	cases := []struct {
		path string
		want InstallKind
	}{
		{"/opt/beacon/bin/beacon", InstallSystemPkg},
		{"/opt/beacon/bin/nested/beacon", InstallSystemPkg},
		{"/opt/homebrew/Cellar/beacon/0.0.1/bin/beacon", InstallHomebrew},
		{"/usr/local/Homebrew/x/beacon", InstallHomebrew},
		{"/home/linuxbrew/.linuxbrew/bin/beacon", InstallHomebrew},
		{"/Users/x/go/bin/beacon", InstallOther},
		{"/tmp/beacon", InstallOther},
	}
	for _, c := range cases {
		got := classifyInstall(c.path)
		if got.Kind != c.want {
			t.Errorf("classifyInstall(%q) = %q, want %q", c.path, got.Kind, c.want)
		}
		if got.BinaryPath != filepath.Clean(c.path) {
			t.Errorf("classifyInstall(%q) path = %q", c.path, got.BinaryPath)
		}
	}
}

func TestSupportsSeamlessUpdate(t *testing.T) {
	if !(Install{Kind: InstallSystemPkg}).SupportsSeamlessUpdate() {
		t.Error("system pkg should support seamless update")
	}
	if (Install{Kind: InstallHomebrew}).SupportsSeamlessUpdate() {
		t.Error("homebrew should not support seamless update")
	}
}

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"auto": ModeAuto, "AUTO": ModeAuto, "1": ModeAuto, "true": ModeAuto, "on": ModeAuto,
		"check-only": ModeCheckOnly, "check": ModeCheckOnly,
		"off": ModeOff, "0": ModeOff, "false": ModeOff, "disable": ModeOff,
	}
	for in, want := range cases {
		got, ok := ParseMode(in)
		if !ok || got != want {
			t.Errorf("ParseMode(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	if _, ok := ParseMode("bogus"); ok {
		t.Error("bogus mode should not parse")
	}
}

func TestResolveModePrecedence(t *testing.T) {
	// Point the managed config at a temp dir so the test is hermetic.
	dir := t.TempDir()
	origManaged := ManagedConfigPath
	ManagedConfigPath = filepath.Join(dir, "managed.json")
	t.Cleanup(func() { ManagedConfigPath = origManaged })

	// No env, no managed, no local => default off (auto-update is opt-in).
	if got := ResolveMode(""); got != ModeOff {
		t.Fatalf("default = %q", got)
	}

	// Local config wins over default.
	if got := ResolveMode("auto"); got != ModeAuto {
		t.Fatalf("local auto = %q", got)
	}

	// Managed overrides local.
	if err := writeFile(ManagedConfigPath, `{"auto_update":{"mode":"check-only"}}`); err != nil {
		t.Fatal(err)
	}
	if got := ResolveMode("off"); got != ModeCheckOnly {
		t.Fatalf("managed override = %q", got)
	}

	// Env overrides everything.
	t.Setenv(AutoUpdateEnv, "off")
	if got := ResolveMode("auto"); got != ModeOff {
		t.Fatalf("env override = %q", got)
	}
}

func TestResolveManifestURLPrecedence(t *testing.T) {
	dir := t.TempDir()
	origManaged := ManagedConfigPath
	ManagedConfigPath = filepath.Join(dir, "managed.json")
	t.Cleanup(func() { ManagedConfigPath = origManaged })

	if got := resolveManifestURL(); got != updatecheck.DefaultManifestURL {
		t.Fatalf("default = %q", got)
	}

	if err := writeFile(ManagedConfigPath, `{"manifest_url":"https://managed.example/m.json"}`); err != nil {
		t.Fatal(err)
	}
	if got := resolveManifestURL(); got != "https://managed.example/m.json" {
		t.Fatalf("managed = %q", got)
	}

	t.Setenv(updatecheck.ManifestURLEnv, "https://env.example/m.json")
	if got := resolveManifestURL(); got != "https://env.example/m.json" {
		t.Fatalf("env = %q", got)
	}
}

func TestCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":1,"version":"9.9.9","artifacts":{` +
			`"` + updatecheck.RuntimeArchKey() + `":{"url":"https://x/p.pkg","sha256":"deadbeef"}}}`))
	}))
	defer srv.Close()
	t.Setenv(updatecheck.ManifestURLEnv, srv.URL)

	res, err := Check(context.Background(), "0.0.1")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.UpdateAvailable {
		t.Fatalf("expected update available: %+v", res)
	}
	if !res.HasArtifact || res.Artifact.URL != "https://x/p.pkg" {
		t.Fatalf("artifact not resolved for running arch: %+v", res)
	}
}

func TestCheckSkipsManifestFetchForDevBuild(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv(updatecheck.ManifestURLEnv, srv.URL)

	res, err := Check(context.Background(), "dev")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if called {
		t.Fatal("manifest server was called for dev build")
	}
	if !res.CurrentIsDev {
		t.Fatalf("expected CurrentIsDev: %+v", res)
	}
}

func TestCheckMissingArtifactIsNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":1,"version":"9.9.9","artifacts":{"darwin_arm64":{"url":"https://x/p.pkg","sha256":"deadbeef"}}}`))
	}))
	defer srv.Close()
	t.Setenv(updatecheck.ManifestURLEnv, srv.URL)

	res, err := Check(context.Background(), "0.0.1")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.UpdateAvailable {
		t.Fatalf("expected update available: %+v", res)
	}
	if res.ArchKey != "darwin_arm64" && res.HasArtifact {
		t.Fatalf("non-arm64 platforms should not resolve an artifact from arm64-only manifest: %+v", res)
	}
}

func TestSystemLogPathFollowsRuntimeLog(t *testing.T) {
	runtimeLog := filepath.Join(t.TempDir(), "runtime.jsonl")
	if got, want := SystemLogPath(runtimeLog, false), filepath.Join(filepath.Dir(runtimeLog), SystemLogFileName); got != want {
		t.Fatalf("SystemLogPath = %q, want %q", got, want)
	}
}

func TestCheckOutcome(t *testing.T) {
	res := CheckResult{
		ManifestResult: updatecheck.ManifestResult{UpdateAvailable: true},
		Install:        Install{Kind: InstallSystemPkg},
		ArchKey:        "darwin_arm64",
		HasArtifact:    true,
	}
	if action, reason := CheckOutcome(res); action != EventAvailable || reason != "update_available" {
		t.Fatalf("available outcome = %s/%s", action, reason)
	}
	res.HasArtifact = false
	if action, reason := CheckOutcome(res); action != EventUnsupported || reason != "no_artifact_for_arch" {
		t.Fatalf("missing artifact outcome = %s/%s", action, reason)
	}
	res.UpdateAvailable = false
	if action, reason := CheckOutcome(res); action != EventCurrent || reason != "already_current" {
		t.Fatalf("current outcome = %s/%s", action, reason)
	}
}

func TestEmitCheckEventWritesSystemLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.jsonl")
	res := CheckResult{
		ManifestResult: updatecheck.ManifestResult{
			CurrentVersion:  "v0.0.76",
			LatestVersion:   "v0.0.77",
			UpdateAvailable: true,
		},
		Install:     Install{Kind: InstallSystemPkg},
		Mode:        ModeCheckOnly,
		ArchKey:     "darwin_arm64",
		Artifact:    updatecheck.Artifact{URL: "https://example.test/p.pkg", SHA256: "abc"},
		HasArtifact: true,
	}
	if err := EmitCheckEvent(CheckEventOptions{Result: res, LogPath: path, AgentVersion: "0.0.76"}); err != nil {
		t.Fatalf("EmitCheckEvent: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if got := event["event"].(map[string]interface{})["action"]; got != EventAvailable {
		t.Fatalf("event.action = %q, want %s", got, EventAvailable)
	}
	raw := event["raw"].(map[string]interface{})
	if raw["component"] != "selfupdate" || raw["artifact_arch"] != "darwin_arm64" {
		t.Fatalf("raw = %#v", raw)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
