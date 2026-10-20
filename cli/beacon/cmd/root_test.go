package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

func TestPrintRootSplash(t *testing.T) {
	cmd := &cobra.Command{Use: "beacon"}
	var out bytes.Buffer
	cmd.SetOut(&out)

	printRootSplash(cmd)

	got := out.String()
	for _, want := range []string{
		"██████╗ ███████╗ █████╗  ██████╗ ██████╗ ███╗   ██╗",
		"Self-improving memory for coding agents.",
		"Start with:",
		"beacon endpoint install",
		"beacon endpoint status",
		"beacon endpoint wazuh print-config",
		"Usage:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("root splash missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("root splash written to a buffer should not include ANSI escapes:\n%q", got)
	}
	for _, line := range beaconBanner {
		if len([]rune(line)) > 64 {
			t.Fatalf("banner line %q is too wide: got %d columns", line, len(line))
		}
	}
}

func TestPrintRootUpdateNotice(t *testing.T) {
	result := updatecheck.Result{
		CurrentVersion:  "v1.3.33",
		LatestVersion:   "v1.3.34",
		UpdateAvailable: true,
	}

	tests := []struct {
		name    string
		install selfupdate.Install
		command string
	}{
		{name: "homebrew", install: selfupdate.Install{Kind: selfupdate.InstallHomebrew}, command: "brew update && brew upgrade beacon"},
		{name: "system package", install: selfupdate.Install{Kind: selfupdate.InstallSystemPkg}, command: "sudo beacon endpoint update --apply"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			printRootUpdateNotice(&out, result, false, tt.install)
			got := out.String()
			for _, want := range []string{
				"Update available · v1.3.33 → v1.3.34",
				"Release notes: https://docs.beacon.sh/changelog/cli",
				"Update with: " + tt.command,
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("update notice missing %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, "\x1b[") {
				t.Fatalf("uncolored update notice includes ANSI escapes: %q", got)
			}
		})
	}
}

func TestPrintRootUpdateNoticeUsesOrange(t *testing.T) {
	var out bytes.Buffer
	printRootUpdateNotice(&out, updatecheck.Result{
		CurrentVersion:  "v1.3.33",
		LatestVersion:   "v1.3.34",
		UpdateAvailable: true,
	}, true, selfupdate.Install{Kind: selfupdate.InstallHomebrew})

	if !strings.HasPrefix(out.String(), beaconOrange) {
		t.Fatalf("colored update notice does not start orange: %q", out.String())
	}
	if !strings.Contains(out.String(), resetColor) {
		t.Fatalf("colored update notice does not reset color: %q", out.String())
	}
}

func TestPrintRootUpdateNoticePointsOtherInstallsAtTheRelease(t *testing.T) {
	other := selfupdate.Install{Kind: selfupdate.InstallOther}
	for _, tt := range []struct {
		name   string
		result updatecheck.Result
		want   string
	}{
		{name: "release url", result: updatecheck.Result{CurrentVersion: "v1.3.33", LatestVersion: "v1.3.34", ReleaseURL: "https://example.test/v1.3.34"}, want: "Download: https://example.test/v1.3.34"},
		{name: "no release url", result: updatecheck.Result{CurrentVersion: "v1.3.33", LatestVersion: "v1.3.34"}, want: "Download: " + beaconReleasesURL},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			printRootUpdateNotice(&out, tt.result, false, other)
			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("notice missing %q:\n%s", tt.want, out.String())
			}
			// `beacon endpoint update` cannot update these installs.
			if strings.Contains(out.String(), "endpoint update") {
				t.Fatalf("notice offers an updater that does not apply:\n%s", out.String())
			}
		})
	}
}

func TestRootUpdateNoticeSkipsNonInteractiveCommands(t *testing.T) {
	oldRefresh := rootStartUpdateRefresh
	t.Cleanup(func() { rootStartUpdateRefresh = oldRefresh })
	called := false
	rootStartUpdateRefresh = func() error {
		called = true
		return nil
	}

	cmd := &cobra.Command{}
	cmd.SetIn(bytes.NewBuffer(nil))
	cmd.SetOut(&bytes.Buffer{})
	maybePrintRootUpdateNotice(cmd)
	if called {
		t.Fatal("non-interactive root command started an update refresh")
	}
}

// isTerminal gates the full-screen onboarding wizard and the trace browser, so
// anything that is not a real terminal has to read as false.
//
// This previously tested os.ModeCharDevice, which /dev/null also sets. The result
// was that `beacon endpoint install < /dev/null` -- an ordinary shape for a wrapper
// script or an unattended run -- looked interactive, started the wizard against an
// input already at EOF, and failed the install with "onboarding cancelled".
func TestIsTerminalRejectsDevNullPipesAndRegularFiles(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()

	regular, err := os.Create(filepath.Join(t.TempDir(), "redirected.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()

	for name, file := range map[string]*os.File{
		"/dev/null":    devNull,
		"pipe read":    readEnd,
		"pipe write":   writeEnd,
		"regular file": regular,
		"nil":          nil,
	} {
		if isTerminal(file) {
			t.Fatalf("isTerminal(%s) = true, want false", name)
		}
	}
}

func TestShowCachedUpdateNoticeReadsTheCacheAndRefreshesWhenStale(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	restoreVersion := setVersionForTest(t, "v1.3.33")
	defer restoreVersion()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	oldNow, oldRefresh, oldDetect := rootNow, rootStartUpdateRefresh, rootDetectInstall
	t.Cleanup(func() { rootNow, rootStartUpdateRefresh, rootDetectInstall = oldNow, oldRefresh, oldDetect })
	rootNow = func() time.Time { return now }
	rootDetectInstall = func() selfupdate.Install { return selfupdate.Install{Kind: selfupdate.InstallHomebrew} }
	refreshes := 0
	rootStartUpdateRefresh = func() error {
		refreshes++
		return nil
	}
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	run := func() string {
		var out bytes.Buffer
		showCachedUpdateNotice(&out, false, getenv)
		return out.String()
	}

	// No cache yet: nothing to show, one background refresh.
	if got := run(); got != "" || refreshes != 1 {
		t.Fatalf("empty cache: output %q, refreshes %d; want no output and 1 refresh", got, refreshes)
	}

	// A fresh cache with a newer release: the notice, no refresh.
	if err := updatecheck.SaveCache(updateCachePath(), updatecheck.Cache{LatestVersion: "v1.3.34", LastCheckedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := run(); !strings.Contains(got, "Update available · v1.3.33 → v1.3.34") || refreshes != 1 {
		t.Fatalf("fresh cache: output %q, refreshes %d; want notice and no refresh", got, refreshes)
	}

	// A stale cache still shows what it knows and schedules a refresh.
	if err := updatecheck.SaveCache(updateCachePath(), updatecheck.Cache{LatestVersion: "v1.3.34", LastCheckedAt: now.Add(-25 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := run(); !strings.Contains(got, "Update available") || refreshes != 2 {
		t.Fatalf("stale cache: output %q, refreshes %d; want notice and a refresh", got, refreshes)
	}

	// The opt-out and CI turn off both the notice and the refresh.
	for _, kv := range [][2]string{{updatecheck.DisableEnv, "0"}, {"CI", "true"}} {
		env = map[string]string{kv[0]: kv[1]}
		if got := run(); got != "" || refreshes != 2 {
			t.Fatalf("%s=%s: output %q, refreshes %d; want nothing", kv[0], kv[1], got, refreshes)
		}
	}
}

func TestShowCachedUpdateNoticeSkipsDevBuilds(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	restoreVersion := setVersionForTest(t, "dev")
	defer restoreVersion()
	oldRefresh := rootStartUpdateRefresh
	t.Cleanup(func() { rootStartUpdateRefresh = oldRefresh })
	rootStartUpdateRefresh = func() error {
		t.Fatal("dev build started an update refresh")
		return nil
	}
	var out bytes.Buffer
	showCachedUpdateNotice(&out, false, func(string) string { return "" })
	if out.Len() != 0 {
		t.Fatalf("dev build printed %q", out.String())
	}
}
