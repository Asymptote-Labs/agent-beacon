package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/check"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/image"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/sandbox"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/scenario"
)

// Exec only returns supplied observations; it never executes a guest script.
type uninstallGuest struct {
	status                   sandbox.Result
	statusErr, operationErr  error
	serviceErr, retentionErr error
}

func (g uninstallGuest) Exec(_ context.Context, script string, _ sandbox.ExecOpts) (sandbox.Result, error) {
	switch {
	case strings.Contains(script, "endpoint status"):
		return g.status, g.statusErr
	case strings.Contains(script, "endpoint uninstall"):
		return sandbox.Result{Stdout: strings.Repeat("removed\n", 1000)}, g.operationErr
	case strings.Contains(script, "list-unit-files"):
		return sandbox.Result{}, g.serviceErr
	default:
		return sandbox.Result{}, g.retentionErr
	}
}

func (uninstallGuest) Get(context.Context, string, string) error { return nil }
func (uninstallGuest) Describe() string                          { return "synthetic uninstall observations" }

func TestUninstallProbePreservesStatusForSavedJudgment(t *testing.T) {
	// The long diagnostic is synthetic, in a documented runtime_log string field. It places the
	// service state beyond both the display limit and 4096 bytes without changing the schema.
	status := `{"version":"1.3.91","config_path":"/etc/beacon/endpoint/config.json","log_path":"/var/log/beacon-agent/runtime.jsonl","runtime_log":{"warning":` +
		`"` + strings.Repeat("synthetic diagnostic; ", 250) + `"},"service":{"running":true,"kind":"systemd"}}`
	if len(status) <= 4096 || strings.Index(status, `"running"`) <= 4096 {
		t.Fatal("fixture must exercise an unclipped status tail")
	}
	for _, running := range []bool{true, false} {
		t.Run(map[bool]string{true: "running", false: "stopped"}[running], func(t *testing.T) {
			stdout := status
			if !running {
				stdout = strings.Replace(stdout, `"running":true`, `"running":false`, 1)
			}
			art := Artifacts{Meta: map[string]string{"service_kind": "systemd", "service_label": "beacon.service", "install_log_path": "/synthetic/runtime.jsonl"}}
			probeUninstall(context.Background(), uninstallGuest{status: sandbox.Result{Stdout: stdout}}, scenario.Scenario{Install: &scenario.Install{Mode: "user"}}, sandbox.ExecOpts{}, sandbox.ExecOpts{}, image.Layout{}, posixShell{}, &art, func(string, ...any) {})
			if art.Meta["uninstall_status"] != stdout {
				t.Errorf("machine status changed: got %d bytes, want %d", len(art.Meta["uninstall_status"]), len(stdout))
			}
			if len(art.Meta["uninstall_output"]) > 403 {
				t.Error("non-JSON human output lost its display bound")
			}
			b, err := json.Marshal(art.Meta)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "meta.json")
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}
			b, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved map[string]string
			if err := json.Unmarshal(b, &saved); err != nil {
				t.Fatal(err)
			}
			if saved["uninstall_status"] != stdout {
				t.Error("saved artifact lost full status")
			}
			var v check.Verdict
			check.Uninstall(&v, check.Removal{Ran: saved["uninstall_ran"], ExitCode: saved["uninstall_rc"], ServiceGone: saved["uninstall_service_gone"], LogRetained: saved["uninstall_log_retained"], Status: saved["uninstall_status"]})
			v.Resolve()
			want := check.Pass
			if running {
				want = check.Fail
			}
			if v.Outcome != want {
				t.Errorf("saved verdict=%s, want %s:\n%s", v.Outcome, want, v.Report())
			}
		})
	}
}

func TestUninstallProbeRecordsFailedObservations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		guest uninstallGuest
		key   string
	}{
		{"status command", uninstallGuest{status: sandbox.Result{ExitCode: 2, Stdout: `{"service":{"running":false}}`}}, "uninstall_status_rc"},
		{"status transport", uninstallGuest{statusErr: errors.New("status transport failed")}, "uninstall_status_exec_error"},
		{"uninstall transport", uninstallGuest{operationErr: errors.New("uninstall transport failed")}, "uninstall_exec_error"},
		{"service transport", uninstallGuest{serviceErr: errors.New("service transport failed")}, "uninstall_service_query_exec_error"},
		{"retention transport", uninstallGuest{retentionErr: errors.New("retention transport failed")}, "uninstall_log_exec_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			art := Artifacts{Meta: map[string]string{"service_kind": "systemd", "service_label": "beacon.service", "install_log_path": "/synthetic/runtime.jsonl"}}
			probeUninstall(context.Background(), tc.guest, scenario.Scenario{Install: &scenario.Install{Mode: "user"}}, sandbox.ExecOpts{}, sandbox.ExecOpts{}, image.Layout{}, posixShell{}, &art, func(string, ...any) {})
			if art.Meta[tc.key] == "" {
				t.Fatalf("failed observation was lost: %s", tc.key)
			}
			if tc.guest.serviceErr != nil && art.Meta["uninstall_service_gone"] != "" {
				t.Error("failed service observation invented a result")
			}
			if tc.guest.retentionErr != nil && art.Meta["uninstall_log_retained"] != "" {
				t.Error("failed retention observation invented a result")
			}
		})
	}
}
