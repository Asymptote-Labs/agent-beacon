package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// These characterization tests pin the externally observable behavior of every
// forwarding-destination command (print-config / install-pack / validate) by
// driving them through the assembled `endpoint` command tree rather than via
// the private run* helpers or per-command package vars. They exist so that the
// destination-registry refactor can delete those internals without silently
// changing the CLI surface: if a generated command stops matching the current
// output, these tests fail.
//
// Keep the expected strings here byte-for-byte identical to what the current
// hand-written commands print.

type destinationCharCase struct {
	// group is the `Use` name of the destination command under `endpoint`.
	group string
	// installLabel is the exact prefix printed after a successful install-pack.
	installLabel string
	// validateWant lists substrings that must appear in validate stdout. A nil
	// slice means the destination has no validate subcommand (elastic today).
	validateWant []string
}

func destinationCharCases() []destinationCharCase {
	return []destinationCharCase{
		{
			group:        "wazuh",
			installLabel: "Wazuh content pack written to ",
			validateWant: []string{
				"Expected Wazuh fields: vendor=beacon product=endpoint-agent event.kind=agent_runtime",
				"Wazuh localfile snippet:",
				"Expected base rule: 100500",
			},
		},
		{
			group:        "elastic",
			installLabel: "Elasticsearch content pack written to ",
			validateWant: nil, // elastic has no validate subcommand
		},
		{
			group:        "datadog",
			installLabel: "Datadog content pack written to ",
			validateWant: []string{
				"Expected Datadog fields: service=beacon-endpoint-agent vendor=beacon product=endpoint-agent",
				`Expected validation query: service:beacon-endpoint-agent "Beacon endpoint datadog validation event"`,
			},
		},
		{
			group:        "sumo",
			installLabel: "Sumo Logic content pack written to ",
			validateWant: []string{
				"Expected Sumo fields: _sourceCategory=security/agentbeacon product=agentbeacon telemetry=ai_agent",
				`Expected validation query: _sourceCategory=security/agentbeacon "Beacon endpoint Sumo validation event"`,
			},
		},
		{
			group:        "rapid7",
			installLabel: "Rapid7 InsightIDR content pack written to ",
			validateWant: []string{
				"Expected Rapid7 fields: vendor=beacon product=endpoint-agent destination.type=rapid7",
				`Expected validation query: "Beacon endpoint Rapid7 validation event"`,
			},
		},
		{
			group:        "s3",
			installLabel: "AWS S3 content pack written to ",
			validateWant: []string{
				"Expected S3 fields: vendor=beacon product=endpoint-agent destination.type=s3 destination.mode=aws_s3_jsonl",
				"aws s3 ls",
				"aws s3 cp",
				"Beacon endpoint S3 validation event",
			},
		},
		{
			group:        "cloudwatch",
			installLabel: "AWS CloudWatch Logs content pack written to ",
			validateWant: []string{
				"Expected AWS CloudWatch Logs fields: vendor=beacon product=endpoint-agent destination.type=cloudwatch destination.mode=aws_cloudwatch_logs",
				"aws logs filter-log-events",
				"CloudWatch Logs Insights query",
				"Beacon endpoint AWS CloudWatch Logs validation event",
			},
		},
		{
			group:        "gcs",
			installLabel: "Google Cloud Storage content pack written to ",
			validateWant: []string{
				"Expected GCS fields: vendor=beacon product=endpoint-agent destination.type=gcs destination.mode=google_cloud_storage_jsonl",
				"gcloud storage ls",
				"gcloud storage cat",
				"Beacon endpoint GCS validation event",
			},
		},
		{
			group:        "sentinel",
			installLabel: "Microsoft Sentinel content pack written to ",
			validateWant: []string{
				"Expected Sentinel table: BeaconRuntime_CL",
				`Expected validation query: BeaconRuntime_CL | where Message has "Beacon endpoint Sentinel validation event"`,
			},
		},
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
				if !strings.Contains(out, logPath) {
					t.Fatalf("%s print-config did not wire --log-path %q through to output:\n%s", tc.group, logPath, out)
				}
			})

			t.Run("install-pack", func(t *testing.T) {
				logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
				outputDir := filepath.Join(t.TempDir(), "pack")
				setEndpointOptsForTest(t, logPath, outputDir)

				out, err := runEndpointLeaf(t, tc.group, "install-pack")
				if err != nil {
					t.Fatalf("%s install-pack returned error: %v", tc.group, err)
				}
				wantLine := tc.installLabel + outputDir
				if !strings.Contains(out, wantLine) {
					t.Fatalf("%s install-pack missing success line %q:\n%s", tc.group, wantLine, out)
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
				if tc.validateWant == nil {
					if leaf := findEndpointLeaf(tc.group, "validate"); leaf != nil {
						t.Fatalf("%s unexpectedly exposes a validate subcommand", tc.group)
					}
					t.Skip("no validate subcommand for this destination")
				}
				logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
				setEndpointOptsForTest(t, logPath, "")

				out, err := runEndpointLeaf(t, tc.group, "validate")
				if err != nil {
					t.Fatalf("%s validate returned error: %v", tc.group, err)
				}
				if !strings.Contains(out, "Validation event written to ") {
					t.Fatalf("%s validate missing 'Validation event written to' line:\n%s", tc.group, out)
				}
				for _, want := range tc.validateWant {
					if !strings.Contains(out, want) {
						t.Fatalf("%s validate missing %q:\n%s", tc.group, want, out)
					}
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
