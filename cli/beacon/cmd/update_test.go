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

type stubUpdateChecker struct {
	result updatecheck.Result
	err    error
}

func (c stubUpdateChecker) Check(context.Context) (updatecheck.Result, error) {
	if c.err != nil {
		return updatecheck.Result{}, c.err
	}
	return c.result, nil
}

func TestVersionCheckCommandRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"version", "check"})
	if err != nil {
		t.Fatalf("Find version check returned error: %v", err)
	}
	if cmd == nil || cmd.Use != "check" {
		t.Fatalf("version check command not registered: %#v", cmd)
	}
}

func TestRunUpdateCheckReportsUpToDate(t *testing.T) {
	got := runUpdateCheckWithStub(t, "v0.0.10", stubUpdateChecker{
		result: updatecheck.Result{CurrentVersion: "v0.0.10", LatestVersion: "v0.0.10"},
	})
	if !strings.Contains(got, "Beacon v0.0.10 is up to date.") {
		t.Fatalf("output = %q, want up-to-date message", got)
	}
}

func TestRunUpdateCheckReportsAvailableUpdate(t *testing.T) {
	got := runUpdateCheckWithStub(t, "v0.0.10", stubUpdateChecker{
		result: updatecheck.Result{
			CurrentVersion:  "v0.0.10",
			LatestVersion:   "v0.0.12",
			ReleaseURL:      "https://example.test/releases/v0.0.12",
			UpdateAvailable: true,
		},
	})
	for _, want := range []string{
		"Beacon v0.0.12 is available. Current version: v0.0.10",
		"Upgrade with Homebrew: brew upgrade beacon",
		"Download: https://example.test/releases/v0.0.12",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
}

func TestRunUpdateCheckReportsDevBuild(t *testing.T) {
	got := runUpdateCheckWithStub(t, "dev", stubUpdateChecker{
		result: updatecheck.Result{CurrentVersion: "dev", CurrentIsDev: true},
	})
	if !strings.Contains(got, "Beacon dev build: update checks require a released version.") {
		t.Fatalf("output = %q, want dev-build message", got)
	}
}

func TestRunUpdateCheckReportsUnsupportedCurrentVersion(t *testing.T) {
	got := runUpdateCheckWithStub(t, "v0.0.12+local", stubUpdateChecker{
		result: updatecheck.Result{CurrentVersion: "v0.0.12+local", UnsupportedCurrentVersion: true},
	})
	if !strings.Contains(got, `Beacon version "v0.0.12+local" cannot be compared to released versions.`) {
		t.Fatalf("output = %q, want unsupported-version message", got)
	}
}

func TestRunUpdateCheckReturnsFriendlyError(t *testing.T) {
	restoreVersion := setVersionForTest(t, "v0.0.10")
	defer restoreVersion()
	restoreChecker := setUpdateCheckerForTest(t, stubUpdateChecker{err: errors.New("rate limited")})
	defer restoreChecker()

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := runUpdateCheck(cmd, nil)
	if err == nil {
		t.Fatal("runUpdateCheck error = nil, want error")
	}
	if !strings.Contains(err.Error(), "unable to check for Beacon updates") {
		t.Fatalf("error = %v, want friendly update error", err)
	}
}

func runUpdateCheckWithStub(t *testing.T, current string, checker stubUpdateChecker) string {
	t.Helper()
	restoreVersion := setVersionForTest(t, current)
	defer restoreVersion()
	restoreChecker := setUpdateCheckerForTest(t, checker)
	defer restoreChecker()

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runUpdateCheck(cmd, nil); err != nil {
		t.Fatalf("runUpdateCheck returned error: %v", err)
	}
	return out.String()
}

func setVersionForTest(t *testing.T, v string) func() {
	t.Helper()
	old := version.Version
	version.Version = v
	return func() { version.Version = old }
}

func setUpdateCheckerForTest(t *testing.T, checker updateChecker) func() {
	t.Helper()
	old := newUpdateChecker
	newUpdateChecker = func(currentVersion string) updateChecker {
		return checker
	}
	return func() { newUpdateChecker = old }
}
