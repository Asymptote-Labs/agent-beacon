package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
)

// These tests drive every forwarding-destination command (print-config /
// install-pack / validate) through the assembled `endpoint` command tree, so a
// destination that stops wiring --log-path, stops writing its pack, or loses its
// validate subcommand fails here.

type destinationCharCase struct {
	// group is the `Use` name of the destination command under `endpoint`.
	group string
	// hasValidate reports whether the destination exposes a validate subcommand.
	hasValidate bool
}

func destinationCharCases() []destinationCharCase {
	return []destinationCharCase{
		{group: "falcon", hasValidate: true},
		{group: "wazuh", hasValidate: true},
		{group: "elastic", hasValidate: false},
		{group: "datadog", hasValidate: true},
		{group: "sumo", hasValidate: true},
		{group: "rapid7", hasValidate: true},
		{group: "s3", hasValidate: true},
		{group: "cloudwatch", hasValidate: true},
		{group: "gcs", hasValidate: true},
		{group: "asymptote", hasValidate: true},
		{group: "sentinel", hasValidate: true},
	}
}

func TestDestinationCommandsCharacterization(t *testing.T) {
	for _, tc := range destinationCharCases() {
		tc := tc
		t.Run(tc.group, func(t *testing.T) {
			t.Run("print-config", func(t *testing.T) {
				logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
				setEndpointOptsForTest(t, logPath, "")

				out, err := runEndpointLeaf(t, tc.group, "print-config")
				if err != nil {
					t.Fatalf("%s print-config returned error: %v", tc.group, err)
				}
				// TOML configs write the path escaped; on Windows that doubles its backslashes.
				if !strings.Contains(out, logPath) && !strings.Contains(out, siempack.TOMLEscape(logPath)) {
					t.Fatalf("%s print-config did not wire --log-path %q through to output:\n%s", tc.group, logPath, out)
				}
			})

			t.Run("install-pack", func(t *testing.T) {
				logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
				outputDir := filepath.Join(t.TempDir(), "pack")
				setEndpointOptsForTest(t, logPath, outputDir)

				if _, err := runEndpointLeaf(t, tc.group, "install-pack"); err != nil {
					t.Fatalf("%s install-pack returned error: %v", tc.group, err)
				}
				entries, err := os.ReadDir(outputDir)
				if err != nil {
					t.Fatalf("%s install-pack did not create output dir %q: %v", tc.group, outputDir, err)
				}
				if len(entries) == 0 {
					t.Fatalf("%s install-pack wrote no files to %q", tc.group, outputDir)
				}
			})

			t.Run("validate", func(t *testing.T) {
				if !tc.hasValidate {
					if leaf := findEndpointLeaf(tc.group, "validate"); leaf != nil {
						t.Fatalf("%s unexpectedly exposes a validate subcommand", tc.group)
					}
					t.Skip("no validate subcommand for this destination")
				}
				logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
				setEndpointOptsForTest(t, logPath, "")

				if _, err := runEndpointLeaf(t, tc.group, "validate"); err != nil {
					t.Fatalf("%s validate returned error: %v", tc.group, err)
				}
				if info, err := os.Stat(logPath); err != nil || info.Size() == 0 {
					t.Fatalf("%s validate did not write a validation event to %s: %v", tc.group, logPath, err)
				}
			})
		})
	}
}

// setEndpointOptsForTest snapshots and restores the whole shared endpointOpts
// struct so destination subtests cannot leak flag state into one another.
func setEndpointOptsForTest(t *testing.T, logPath, outputDir string) {
	t.Helper()
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })
	endpointOpts.logPath = logPath
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	endpointOpts.outputDir = outputDir
}

// findEndpointLeaf walks the assembled endpoint command tree to locate a
// subcommand by group and leaf name. It returns nil if either is absent.
func findEndpointLeaf(group, leaf string) *cobra.Command {
	var groupCmd *cobra.Command
	for _, c := range endpointCmd.Commands() {
		if c.Name() == group {
			groupCmd = c
			break
		}
	}
	if groupCmd == nil {
		return nil
	}
	for _, c := range groupCmd.Commands() {
		if c.Name() == leaf {
			return c
		}
	}
	return nil
}

// runEndpointLeaf invokes a destination subcommand the same way cobra would,
// tolerating both Run and RunE handlers, and captures its stdout.
func runEndpointLeaf(t *testing.T, group, leaf string) (string, error) {
	t.Helper()
	cmd := findEndpointLeaf(group, leaf)
	if cmd == nil {
		t.Fatalf("endpoint %s %s subcommand is not registered", group, leaf)
	}
	return captureStdout(t, func() error {
		switch {
		case cmd.RunE != nil:
			return cmd.RunE(cmd, nil)
		case cmd.Run != nil:
			cmd.Run(cmd, nil)
			return nil
		default:
			return fmt.Errorf("endpoint %s %s has no Run or RunE handler", group, leaf)
		}
	})
}
