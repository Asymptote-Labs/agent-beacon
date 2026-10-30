package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/check"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/image"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/sandbox"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/scenario"
)

// Exec answers `endpoint status` with the supplied result and every other script with success.
type uninstallGuest struct{ status sandbox.Result }

func (g uninstallGuest) Exec(_ context.Context, script string, _ sandbox.ExecOpts) (sandbox.Result, error) {
	if strings.Contains(script, "endpoint status") {
		return g.status, nil
	}
	return sandbox.Result{}, nil
}

func (uninstallGuest) Get(context.Context, string, string) error { return nil }
func (uninstallGuest) Describe() string                          { return "synthetic uninstall observations" }

func TestUninstallProbePreservesStatusForSavedJudgment(t *testing.T) {
	// The long diagnostic places service.running well past the runner's display limit, as the
	// real status output does.
	status := `{"version":"1.3.91","config_path":"/etc/beacon/endpoint/config.json","log_path":"/var/log/beacon-agent/runtime.jsonl","runtime_log":{"warning":` +
		`"` + strings.Repeat("synthetic diagnostic; ", 250) + `"},"service":{"running":true,"kind":"systemd"}}`
	for _, running := range []bool{true, false} {
		t.Run(map[bool]string{true: "running", false: "stopped"}[running], func(t *testing.T) {
			stdout := status
			if !running {
				stdout = strings.Replace(stdout, `"running":true`, `"running":false`, 1)
			}
			art := Artifacts{Meta: map[string]string{"service_kind": "systemd", "service_label": "beacon.service", "install_log_path": "/synthetic/runtime.jsonl"}}
			probeUninstall(context.Background(), uninstallGuest{status: sandbox.Result{Stdout: stdout}}, scenario.Scenario{Install: &scenario.Install{Mode: "user"}}, sandbox.ExecOpts{}, sandbox.ExecOpts{}, image.Layout{}, posixShell{}, &art, func(string, ...any) {})
			if art.Meta["uninstall_status"] != stdout {
				t.Errorf("status changed: got %d bytes, want %d", len(art.Meta["uninstall_status"]), len(stdout))
			}
			var v check.Verdict
			check.Uninstall(&v, check.Removal{Ran: art.Meta["uninstall_ran"], ExitCode: art.Meta["uninstall_rc"], ServiceGone: art.Meta["uninstall_service_gone"], LogRetained: art.Meta["uninstall_log_retained"], Status: art.Meta["uninstall_status"]})
			v.Resolve()
			want := check.Pass
			if running {
				want = check.Fail
			}
			if v.Outcome != want {
				t.Errorf("verdict=%s, want %s:\n%s", v.Outcome, want, v.Report())
			}
		})
	}
}
