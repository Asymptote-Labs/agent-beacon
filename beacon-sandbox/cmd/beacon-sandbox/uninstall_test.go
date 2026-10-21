package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/check"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/credentials"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/runner"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/scenario"
)

func TestUninstallStatusOfflineJudgmentParity(t *testing.T) {
	for _, tc := range []struct {
		name, status, rc, statusErr, operationErr string
		want                                      check.Severity
	}{
		{"long running", `{"runtime_log":{"warning":"` + strings.Repeat("synthetic diagnostic; ", 250) + `"},"service":{"running":true}}`, "0", "", "", check.SevFail},
		{"pretty running", "{\n \"service\": {\"running\": true}\n}", "0", "", "", check.SevFail},
		{"legacy stopped", `{"service":{"running":false}}`, "", "", "", ""},
		{"missing status", "", "0", "", "", check.SevWarn},
		{"malformed status", `{"service":`, "0", "", "", check.SevWarn},
		{"failed status command", `{"service":{"running":false}}`, "2", "", "", check.SevWarn},
		{"failed status transport", `{"service":{"running":false}}`, "0", "transport failed", "", check.SevWarn},
		{"failed uninstall transport", `{"service":{"running":false}}`, "0", "", "uninstall transport failed", check.SevFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "run")
			scenarios := filepath.Join(root, "beacon-sandbox", "scenarios")
			for _, p := range []string{dir, scenarios} {
				if err := os.MkdirAll(p, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(scenarios, "uninstall.yaml"), []byte("id: uninstall\nprompt: hello\nexpect:\n  - action: prompt.submitted\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(dir, "runtime.jsonl")
			const event = `{"timestamp":"2026-08-02T18:00:00.123Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"prompt.submitted","category":"prompt"},"severity":"info","endpoint":{"os":"linux","hostname":"s"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"prompt":{"text":"MARKER"},"message":"m"}`
			if err := os.WriteFile(logPath, []byte(event+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			meta := map[string]string{
				"scenario": "uninstall", "canary": "MARKER",
				"host_state": "host state unchanged", "argv_check_ran": "true", "secret_in_argv": "false",
				"session_known": "true", "session_ok": "true", "uninstall_ran": "true", "uninstall_rc": "0",
				"uninstall_service_gone": "true", "uninstall_log_retained": "true",
				"uninstall_status": tc.status, "uninstall_status_rc": tc.rc,
				"uninstall_status_exec_error": tc.statusErr, "uninstall_exec_error": tc.operationErr,
			}
			b, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o600); err != nil {
				t.Fatal(err)
			}
			sc := scenario.Scenario{ID: "uninstall", Prompt: "hello", Expect: []scenario.Expect{{Action: "prompt.submitted"}}}
			initial, _ := judge(sc, runner.Artifacts{Dir: dir, RuntimeLog: logPath, Meta: meta, Canary: "MARKER", SessionKnown: true, SessionOK: true, ArgvCheckRan: true}, credentials.Resolved{}, "")
			offline, err := judgeRunDir(root, dir, "")
			if err != nil {
				t.Fatal(err)
			}
			selected := func(v check.Verdict) []check.Finding {
				var out []check.Finding
				for _, f := range v.Findings {
					if strings.HasPrefix(f.Check, "uninstall.") {
						out = append(out, f)
					}
				}
				return out
			}
			if !reflect.DeepEqual(selected(initial), selected(offline)) {
				t.Fatalf("initial/offline uninstall findings differ:\n%+v\n%+v", selected(initial), selected(offline))
			}
			key := "uninstall.status_agrees"
			if tc.operationErr != "" {
				key = "uninstall.command_succeeded"
			}
			found := false
			for _, f := range selected(offline) {
				if f.Check == key && f.Severity == tc.want {
					found = true
				}
			}
			if tc.want != "" && !found {
				t.Errorf("missing %s/%s:\n%s", key, tc.want, offline.Report())
			}
			if tc.want == "" && offline.Outcome != check.Pass {
				t.Errorf("valid legacy stopped status failed:\n%s", offline.Report())
			}
			for _, name := range []string{"verdict.json", "fingerprint.json"} {
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					t.Errorf("saved %s: %v", name, err)
				}
			}
			b, err = os.ReadFile(filepath.Join(dir, "verdict.json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved check.Verdict
			if err := json.Unmarshal(b, &saved); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(selected(saved), selected(offline)) {
				t.Error("saved verdict dropped uninstall evidence")
			}
			if len(offline.Report()) > 3500 {
				t.Error("human report expanded raw machine status")
			}
		})
	}
}
