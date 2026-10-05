package cmd

import (
	"reflect"
	"runtime"
	"testing"
)

// ompDeviceCallPayloads is the four-event sequence Oh My Pi sends when the model calls an MCP tool
// through its device transport, as captured from a live session: the write that carries the call,
// the device's own tool_call and tool_result reported from inside the dispatch, and the write's
// result. All four share one toolCallId.
func ompDeviceCallPayloads() []map[string]interface{} {
	common := func(payload map[string]interface{}) map[string]interface{} {
		payload["sessionId"] = "sess-1"
		payload["cwd"] = "/repo"
		payload["toolCallId"] = "toolu_013mHwV28HzK9k41kDjbq2HB"
		return payload
	}
	inner := map[string]interface{}{
		"serverName": "beacon-managed", "mcpToolName": "beacon_lookup",
		"provider": "native", "providerName": "OMP",
	}
	args := map[string]interface{}{"kind": "harness", "limit": float64(5)}
	write := map[string]interface{}{
		"path": "xd://mcp__beacon_managed_beacon_lookup", "content": `{"kind": "harness", "limit": 5}`,
	}
	return []map[string]interface{}{
		common(map[string]interface{}{"type": "tool_call", "toolName": "write", "input": write}),
		common(map[string]interface{}{"type": "tool_call", "toolName": "mcp__beacon_managed_beacon_lookup", "input": args}),
		common(map[string]interface{}{
			"type": "tool_result", "toolName": "mcp__beacon_managed_beacon_lookup", "input": args,
			"isError": false, "details": inner,
		}),
		common(map[string]interface{}{
			"type": "tool_result", "toolName": "write", "input": write, "isError": false,
			"details": map[string]interface{}{"xdev": map[string]interface{}{
				"tool": "mcp__beacon_managed_beacon_lookup", "mode": "execute", "tier": "write",
				"args": args, "inner": inner,
			}},
		}),
	}
}

// An MCP call made through a device is recorded the way every other runtime records an MCP call:
// one tool.invoked and one mcp.tool_invoked, attributed to the server that served it -- not a
// file.created for an `xd://` path, and not a second copy of the call under the name `write`.
func TestOmpDeviceCallIsRecordedOnceAsTheDevice(t *testing.T) {
	logPath := ompTestLog(t)
	for _, payload := range ompDeviceCallPayloads() {
		runHookWithInput(t, runOmpEvent, payload)
	}

	if got, want := ompEventActions(t, logPath), []string{"tool.invoked", "mcp.tool_invoked"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	for _, event := range endpointEvents(t, logPath) {
		if tool := nested(t, event, "tool"); tool["name"] != "mcp__beacon_managed_beacon_lookup" {
			t.Fatalf("tool = %v, want the device that ran", tool)
		}
		if file, ok := event["file"]; ok {
			t.Fatalf("a device call grew a file block: %v", file)
		}
	}
	invoked := ompEventWithAction(t, logPath, "mcp.tool_invoked")
	if mcp := nested(t, invoked, "mcp"); mcp["server"] != "beacon-managed" || mcp["tool"] != "beacon_lookup" {
		t.Fatalf("mcp = %v, want the beacon-managed server and its beacon_lookup tool", mcp)
	}
}

// Devices Oh My Pi handles in-process send no report of their own, so the write is the only record
// of the call -- and it is still recorded as the device rather than as a file write.
func TestOmpInProcessDeviceIsRecordedAsTheDevice(t *testing.T) {
	logPath := ompTestLog(t)
	write := map[string]interface{}{"path": "xd://report_issue", "content": `{"report": "grep: bad path list"}`}
	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "tool_call", "toolName": "write", "toolCallId": "call-1", "sessionId": "sess-1", "input": write,
	})
	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "write", "toolCallId": "call-1", "sessionId": "sess-1", "input": write,
		"details": map[string]interface{}{"xdev": map[string]interface{}{
			"tool": "report_issue", "mode": "execute", "args": map[string]interface{}{"report": "grep: bad path list"},
		}},
	})

	if got, want := ompEventActions(t, logPath), []string{"tool.invoked", "tool.completed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	for _, event := range endpointEvents(t, logPath) {
		if tool := nested(t, event, "tool"); tool["name"] != "report_issue" {
			t.Fatalf("tool = %v, want report_issue", tool)
		}
		if file, ok := event["file"]; ok {
			t.Fatalf("a device call grew a file block: %v", file)
		}
	}
}

// Oh My Pi's read and write tools also take URIs: web pages, the runtime's own resources, and a
// device's documentation. None of those is a file, so none produces a file block or a file action.
// The target stays on tool.path, so the row still says what was read.
func TestOmpResourceTargetsAreNotFileActivity(t *testing.T) {
	for _, tc := range []struct {
		tool, target, content string
	}{
		// A URL whose path matches the secret-file pattern: as a file.read it would satisfy the first
		// step of secret-read-then-egress.
		{"read", "https://example.com/config/secrets", ""},
		{"read", "artifact://13", ""},
		{"read", "skill://self-verify-beacon-in-sandbox", ""},
		{"read", "omp://mcp-config.md", ""},
		{"read", "xd://mcp__beacon_managed_beacon_lookup", ""},
		{"write", "proc://bg_3/kill", ""},
		{"write", "local://notes.md", "draft"},
		// A device write asking for help is a documentation lookup, not a call of the device.
		{"write", "xd://lsp", "?"},
	} {
		t.Run(tc.tool+" "+tc.target, func(t *testing.T) {
			args := map[string]interface{}{"path": tc.target, "content": tc.content}
			for typ, action := range map[string]string{"tool_call": "tool.invoked", "tool_result": "tool.completed"} {
				events := ompRuntime.endpointEvents(map[string]interface{}{
					"type": typ, "toolName": tc.tool, "toolCallId": "call-1", "input": args,
				}, "sess-1")
				if len(events) != 1 {
					t.Fatalf("%s produced %d events, want 1", typ, len(events))
				}
				if events[0].action != action {
					t.Fatalf("%s action = %q, want %q", typ, events[0].action, action)
				}
				if file, ok := events[0].fields["file"]; ok {
					t.Fatalf("%s grew a file block: %v", typ, file)
				}
				tool, _ := events[0].fields["tool"].(map[string]interface{})
				if tool["name"] != tc.tool || tool["path"] != tc.target {
					t.Fatalf("%s tool = %v, want %s on %s", typ, tool, tc.tool, tc.target)
				}
			}
		})
	}
}

// A `file://` URI is a filesystem path spelled as a URI, and is recorded as the path it names on
// the host; fileURLPath's own test covers the other platform's reading.
func TestOmpFileURIIsRecordedAsItsPath(t *testing.T) {
	uri, want := "file:///repo/main.go", "/repo/main.go"
	if runtime.GOOS == "windows" {
		uri, want = "file:///C:/repo/main.go", `C:\repo\main.go`
	}
	events := ompRuntime.endpointEvents(map[string]interface{}{
		"type": "tool_result", "toolName": "read", "input": map[string]interface{}{"path": uri},
	}, "sess-1")
	if len(events) != 1 || events[0].action != "file.read" {
		t.Fatalf("events = %+v, want one file.read", events)
	}
	if file, _ := events[0].fields["file"].(map[string]interface{}); file["path"] != want {
		t.Fatalf("file = %v, want %s", file, want)
	}
}
