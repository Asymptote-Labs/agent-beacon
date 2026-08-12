package cmd

import (
	"os"
	"path/filepath"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
)

func stubConsoleUser(t *testing.T, info consoleUserInfo, ok bool, err error) {
	t.Helper()
	restore := activeConsoleUser
	t.Cleanup(func() { activeConsoleUser = restore })
	activeConsoleUser = func() (consoleUserInfo, bool, error) { return info, ok, err }
}

func userHomeWith(t *testing.T, rel, contents string) consoleUserInfo {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return consoleUserInfo{Username: "alice", HomeDir: home}
}

// The state this check exists to catch, and the one that shipped: a system install that resolved
// nobody, so the collector runs perfectly and captures nothing. Every other check is green in this
// state because they all read root's configuration, which the install did write.
func TestConsoleUserConfigCheckFailsWhenTheUserIsNotConfigured(t *testing.T) {
	stubConsoleUser(t, consoleUserInfo{Username: "alice", HomeDir: t.TempDir()}, true, nil)

	got := consoleUserConfigCheck()
	if got.Status != diagnostics.StatusFail {
		t.Errorf("status = %v, want fail: an endpoint capturing nobody must not read as healthy", got.Status)
	}
	if got.Action == "" {
		t.Error("a failure the operator can fix must say how")
	}
}

func TestConsoleUserConfigCheckPassesWhenPointedAtTheCollector(t *testing.T) {
	info := userHomeWith(t, ".claude/settings.json",
		`{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"http://127.0.0.1:4317"}}`)
	stubConsoleUser(t, info, true, nil)

	if got := consoleUserConfigCheck(); got.Status != diagnostics.StatusOK {
		t.Errorf("status = %v (%s), want ok", got.Status, got.Message)
	}
}

// A settings file that exists but points somewhere else is not configured for this endpoint. The
// distinction matters because a developer who has used Claude Code before installing Beacon
// already has the file.
func TestConsoleUserConfigCheckRejectsAnUnrelatedSettingsFile(t *testing.T) {
	info := userHomeWith(t, ".claude/settings.json", `{"theme":"dark"}`)
	stubConsoleUser(t, info, true, nil)

	if got := consoleUserConfigCheck(); got.Status != diagnostics.StatusFail {
		t.Errorf("status = %v, want fail for a settings file not pointed at this collector", got.Status)
	}
}

// An unattended host legitimately has nobody to configure. That is a warning, not a failure --
// failing it would make every CI and container install red for a condition nobody can act on.
func TestConsoleUserConfigCheckWarnsWhenNobodyIsLoggedIn(t *testing.T) {
	stubConsoleUser(t, consoleUserInfo{}, false, nil)

	got := consoleUserConfigCheck()
	if got.Status != diagnostics.StatusWarn {
		t.Errorf("status = %v, want warn when no console user exists", got.Status)
	}
}

// An install with --otlp-grpc-port writes that port into the user's settings. A check that only
// knew the defaults would call a correctly configured user unconfigured -- a false failure in the
// one check whose entire value is being believed when it says something is wrong.
func TestConsoleUserConfigCheckHonoursCustomOTLPPorts(t *testing.T) {
	info := userHomeWith(t, ".claude/settings.json",
		`{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"http://127.0.0.1:5317"}}`)

	cfg := endpointconfig.Config{}
	cfg.Collector.GRPCPort = 5317
	cfg.Collector.HTTPPort = 5318

	if ok, detail := consoleUserHarnessConfigured(info, cfg); !ok {
		t.Errorf("a user pointed at the configured port read as unconfigured: %s", detail)
	}

	// The same settings against an endpoint on the default ports is genuinely not configured for
	// it, and must still say so.
	def := endpointconfig.Config{}
	def.Collector.GRPCPort = endpointconfig.DefaultGRPCPort
	def.Collector.HTTPPort = endpointconfig.DefaultHTTPPort
	if ok, _ := consoleUserHarnessConfigured(info, def); ok {
		t.Error("settings pointed at :5317 matched an endpoint listening on :4317")
	}
}

// Beacon writes 127.0.0.1, but a user or an MDM profile may have written localhost against the
// same collector. Reporting that as unconfigured would be wrong.
func TestConsoleUserConfigCheckAcceptsLocalhostSpelling(t *testing.T) {
	info := userHomeWith(t, ".claude/settings.json",
		`{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"http://localhost:4317"}}`)

	cfg := endpointconfig.Config{}
	cfg.Collector.GRPCPort = endpointconfig.DefaultGRPCPort
	cfg.Collector.HTTPPort = endpointconfig.DefaultHTTPPort

	if ok, detail := consoleUserHarnessConfigured(info, cfg); !ok {
		t.Errorf("the localhost spelling read as unconfigured: %s", detail)
	}
}

// A port is a number, not a string prefix. Matching `host:port` as a substring has no right-hand
// boundary, so an endpoint on port 431 would accept a file pointing at 4317 -- a false OK on the
// check that is trusted when it says the user *is* captured. That direction is the dangerous one:
// a false failure sends someone to run a repair, a false pass tells them a silent capture gap is
// fine.
func TestConsoleUserConfigCheckDoesNotMatchAPortPrefix(t *testing.T) {
	settings := `{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"http://127.0.0.1:4317"}}`

	for name, tc := range map[string]struct {
		grpc, http int
		want       bool
	}{
		"exact port matches":            {4317, 4318, true},
		"prefix of the configured port": {431, 432, false},
		"configured port is longer":     {43170, 43180, false},
		"unrelated ports":               {9999, 9998, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := targetsCollectorPort(settings, tc.grpc, tc.http); got != tc.want {
				t.Errorf("targetsCollectorPort(:%d/:%d) = %v, want %v", tc.grpc, tc.http, got, tc.want)
			}
		})
	}
}

// The three loopback spellings a runtime config could legitimately carry, all against the same
// collector.
func TestConsoleUserConfigCheckAcceptsEveryLoopbackSpelling(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:4317", "localhost:4317", "[::1]:4317"} {
		if !targetsCollectorPort(`{"endpoint":"http://`+addr+`"}`, 4317, 4318) {
			t.Errorf("%s did not read as pointed at the collector", addr)
		}
	}
}

// Hook-only runtimes count as configured: the installed hook command names the hook binary, which
// is what points that runtime at the endpoint.
func TestConsoleUserConfigCheckAcceptsAHookOnlyRuntime(t *testing.T) {
	info := userHomeWith(t, ".cursor/hooks.json",
		`{"hooks":{"beforeShellExecution":[{"command":"/etc/beacon/endpoint/hooks/beacon-hooks pre-tool"}]}}`)
	stubConsoleUser(t, info, true, nil)

	if got := consoleUserConfigCheck(); got.Status != diagnostics.StatusOK {
		t.Errorf("status = %v (%s), want ok for a hook-configured runtime", got.Status, got.Message)
	}
}
