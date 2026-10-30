package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The security property behind sending Devin CLI's PermissionRequest down the default path.
//
// Devin reads `{"decision":"approve"}` from a PermissionRequest hook as the operator's yes, so a
// telemetry hook that printed it would grant every permission Devin was about to ask a person
// for -- including for the command below -- on a runtime where Beacon was installed only to watch.
// The empty object carries no decision and leaves Devin's own prompt in charge. isDevinLikePlatform
// routes both installer spellings (devin, devin-cli) together, so one stands for both.
func TestDevinPermissionRequestDoesNotApproveOnBehalfOfTheUser(t *testing.T) {
	for _, platform := range []string{"devin-cli"} {
		t.Run(platform, func(t *testing.T) {
			setupHookConfigDirs(t)
			platformFlag = platform
			logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
			t.Setenv("BEACON_ENDPOINT_MODE", "1")
			t.Setenv("BEACON_ENDPOINT_LOG", logPath)

			out := runHookWithInput(t, runPermissionRequest, map[string]interface{}{
				"session_id":      "devin-session-1",
				"cwd":             "/tmp/devin-project",
				"hook_event_name": "PermissionRequest",
				"tool_name":       "exec",
				"tool_input":      map[string]interface{}{"command": "rm -rf ~"},
			})
			if len(out) != 0 {
				t.Fatalf("permission response = %#v, want an empty object carrying no decision", out)
			}
			if _, ok := out["decision"]; ok {
				t.Fatalf("permission response carried a decision: %#v", out)
			}

			// Observed as a request, not recorded as an approval nobody gave.
			event := lastEndpointEvent(t, logPath)
			eventFields := event["event"].(map[string]interface{})
			if action := eventFields["action"]; action != "approval.requested" {
				t.Fatalf("event.action = %q, want approval.requested", action)
			}
			approval := event["approval"].(map[string]interface{})
			if approval["decision"] != "requested" {
				t.Fatalf("approval = %#v, want decision=requested", approval)
			}
			if got := event["command"].(map[string]interface{})["command"]; got != "rm -rf ~" {
				t.Fatalf("command = %q, want the requested command recorded", got)
			}
		})
	}
}

// A payload Beacon cannot parse is no grounds for answering on the operator's behalf either. Before
// this was fixed, unreadable stdin was the one input on which Devin got an unconditional approve.
func TestDevinPermissionRequestDoesNotApproveMalformedInput(t *testing.T) {
	for _, platform := range []string{"devin-cli"} {
		t.Run(platform, func(t *testing.T) {
			setupHookConfigDirs(t)
			platformFlag = platform
			logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
			t.Setenv("BEACON_ENDPOINT_MODE", "1")
			t.Setenv("BEACON_ENDPOINT_LOG", logPath)

			out := runHookWithRawInput(t, runPermissionRequest, []byte(`{"tool_name": "exec", "tool_input": `))
			if len(bytes.TrimSpace(out)) != 0 {
				var decoded map[string]interface{}
				if err := json.Unmarshal(out, &decoded); err != nil {
					t.Fatalf("permission response %q is not JSON: %v", out, err)
				}
				if len(decoded) != 0 {
					t.Fatalf("permission response = %#v, want an empty object carrying no decision", decoded)
				}
			}

			// Nothing parsed, so nothing is recorded -- and certainly not an approval.
			if data, err := os.ReadFile(logPath); err == nil && len(bytes.TrimSpace(data)) != 0 {
				for _, event := range endpointEvents(t, logPath) {
					if action := event["event"].(map[string]interface{})["action"]; action == "approval.allowed" {
						t.Fatalf("malformed input was logged as approval.allowed: %#v", event)
					}
				}
			}
		})
	}
}
