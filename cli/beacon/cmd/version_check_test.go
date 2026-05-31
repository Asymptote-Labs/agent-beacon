package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

type stubVersionChecker struct {
	result updatecheck.Result
	err    error
}

func (c stubVersionChecker) Check(context.Context) (updatecheck.Result, error) {
	if c.err != nil {
		return updatecheck.Result{}, c.err
	}
	return c.result, nil
}

func TestVersionCheckFlagRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"version"})
	if err != nil {
		t.Fatalf("Find version returned error: %v", err)
	}
	if cmd == nil {
		t.Fatal("version command not registered")
	}
	if cmd.Flags().Lookup("check") == nil {
		t.Fatal("version command missing --check flag")
	}
}

func TestVersionCheckSubcommandRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"version", "check"})
	if err != nil {
		t.Fatalf("Find version check returned error: %v", err)
	}
	if cmd == nil {
		t.Fatal("version check subcommand not registered")
	}
	if cmd.Use != "check" {
		t.Fatalf("found command Use = %q, want %q", cmd.Use, "check")
	}
}

func TestRunVersionWithoutCheckPrintsVersionOnly(t *testing.T) {
	restoreVersion := setVersionForTest(t, "v0.0.10")
	defer restoreVersion()
	restoreFlag := setVersionCheckForTest(t, false)
	defer restoreFlag()
	restoreChecker := setVersionCheckerForTest(t, stubVersionChecker{err: errors.New("should not run")})
	defer restoreChecker()

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runVersion(cmd, nil); err != nil {
		t.Fatalf("runVersion returned error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "beacon version v0.0.10") {
		t.Fatalf("output = %q, want version", got)
	}
	if strings.Contains(got, "unable to check") {
		t.Fatalf("output = %q, update check unexpectedly ran", got)
	}
}

func TestRunVersionWithCheckPrintsVersionAndUpdateResult(t *testing.T) {
	restoreVersion := setVersionForTest(t, "v0.0.10")
	defer restoreVersion()
	restoreFlag := setVersionCheckForTest(t, true)
	defer restoreFlag()
	restoreChecker := setVersionCheckerForTest(t, stubVersionChecker{
		result: updatecheck.Result{CurrentVersion: "v0.0.10", LatestVersion: "v0.0.10"},
	})
	defer restoreChecker()

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runVersion(cmd, nil); err != nil {
		t.Fatalf("runVersion returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"beacon version v0.0.10",
		"Beacon v0.0.10 is up to date.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
}

func TestRunVersionCheckReportsUpToDate(t *testing.T) {
	got := runVersionCheckWithStub(t, "v0.0.10", stubVersionChecker{
		result: updatecheck.Result{CurrentVersion: "v0.0.10", LatestVersion: "v0.0.10"},
	})
	if !strings.Contains(got, "Beacon v0.0.10 is up to date.") {
		t.Fatalf("output = %q, want up-to-date message", got)
	}
}

func TestRunVersionCheckReportsAvailableUpdate(t *testing.T) {
	got := runVersionCheckWithStub(t, "v0.0.10", stubVersionChecker{
		result: updatecheck.Result{
			CurrentVersion:  "v0.0.10",
			LatestVersion:   "v0.0.12",
			ReleaseURL:      "https://example.test/releases/v0.0.12",
			UpdateAvailable: true,
		},
	})
	for _, want := range []string{
		"Beacon v0.0.12 is available. Current version: v0.0.10",
		"If installed with Homebrew: brew upgrade beacon",
		"Download: https://example.test/releases/v0.0.12",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
}

func TestRunVersionCheckReportsDevBuild(t *testing.T) {
	got := runVersionCheckWithStub(t, "dev", stubVersionChecker{
		result: updatecheck.Result{CurrentVersion: "dev", CurrentIsDev: true},
	})
	if !strings.Contains(got, "Beacon dev build: update checks require a released version.") {
		t.Fatalf("output = %q, want dev-build message", got)
	}
}

func TestRunVersionCheckReportsUnsupportedCurrentVersion(t *testing.T) {
	got := runVersionCheckWithStub(t, "v0.0.12+local", stubVersionChecker{
		result: updatecheck.Result{CurrentVersion: "v0.0.12+local", UnsupportedCurrentVersion: true},
	})
	if !strings.Contains(got, `Beacon version "v0.0.12+local" cannot be compared to released versions.`) {
		t.Fatalf("output = %q, want unsupported-version message", got)
	}
}

func TestRunVersionCheckReturnsFriendlyError(t *testing.T) {
	restoreVersion := setVersionForTest(t, "v0.0.10")
	defer restoreVersion()
	restoreChecker := setVersionCheckerForTest(t, stubVersionChecker{err: errors.New("rate limited")})
	defer restoreChecker()

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := runVersionCheck(cmd, nil)
	if err == nil {
		t.Fatal("runVersionCheck error = nil, want error")
	}
	if !strings.Contains(err.Error(), "unable to check for Beacon updates") {
		t.Fatalf("error = %v, want friendly update error", err)
	}
}

func runVersionCheckWithStub(t *testing.T, current string, checker stubVersionChecker) string {
	t.Helper()
	restoreVersion := setVersionForTest(t, current)
	defer restoreVersion()
	restoreChecker := setVersionCheckerForTest(t, checker)
	defer restoreChecker()

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runVersionCheck(cmd, nil); err != nil {
		t.Fatalf("runVersionCheck returned error: %v", err)
	}
	return out.String()
}

func setVersionForTest(t *testing.T, v string) func() {
	t.Helper()
	old := version.Version
	version.Version = v
	return func() { version.Version = old }
}

func setVersionCheckerForTest(t *testing.T, checker versionChecker) func() {
	t.Helper()
	old := newVersionChecker
	newVersionChecker = func(currentVersion string) versionChecker {
		return checker
	}
	return func() { newVersionChecker = old }
}

func setVersionCheckForTest(t *testing.T, enabled bool) func() {
	t.Helper()
	old := versionCheck
	versionCheck = enabled
	return func() { versionCheck = old }
}
