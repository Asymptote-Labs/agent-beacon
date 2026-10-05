package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func ompFileOf(t *testing.T, events []normalizedEvent) map[string]interface{} {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("produced %d events, want 1", len(events))
	}
	file, ok := events[0].fields["file"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s has no file block: %v", events[0].action, events[0].fields)
	}
	return file
}

// A result names the file the runtime actually touched, and that is the path recorded: absolute,
// with the selector and `~` the model wrote already resolved by the runtime. tool.path keeps what
// was written.
func TestOmpResultRecordsTheFileTheRuntimeResolved(t *testing.T) {
	for _, tc := range []struct {
		tool, target string
		details      map[string]interface{}
		want         string
	}{
		{"read", ".env:1-20", map[string]interface{}{"meta": map[string]interface{}{
			"source": map[string]interface{}{"type": "path", "value": "/repo/.env"},
		}}, "/repo/.env"},
		{"write", "notes.md", map[string]interface{}{"resolvedPath": "/repo/notes.md"}, "/repo/notes.md"},
		{"edit", "main.go", map[string]interface{}{"path": "/repo/main.go", "diff": "-a\n+b"}, "/repo/main.go"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			events := ompRuntime.endpointEvents(map[string]interface{}{
				"type": "tool_result", "toolName": tc.tool, "cwd": "/elsewhere",
				"input": map[string]interface{}{"path": tc.target}, "details": tc.details,
			}, "sess-1")
			if file := ompFileOf(t, events); file["path"] != tc.want {
				t.Fatalf("file.path = %v, want %s", file["path"], tc.want)
			}
			if tool, _ := events[0].fields["tool"].(map[string]interface{}); tool["path"] != tc.target {
				t.Fatalf("tool.path = %v, want %s as written", tool["path"], tc.target)
			}
		})
	}
}

// Before the call runs, and on the approval for it, only the path as written exists. It is resolved
// the way the runtime will resolve it, so the approval rules -- which anchor their patterns at the
// end of the name -- see `/repo/.env` rather than `.env:1-20`.
func TestOmpPathAsWrittenIsResolvedTheWayTheRuntimeWill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	// A file whose real name looks like a selector is kept whole, as the runtime keeps it.
	if err := os.WriteFile(filepath.Join(cwd, "report:2024"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ target, want string }{
		{".env:1-20", filepath.Join(cwd, ".env")},
		{"src/a.go:5-16,960-973", filepath.Join(cwd, "src/a.go")},
		{"notes.md:1-50:raw", filepath.Join(cwd, "notes.md")},
		{"log.txt:-60", filepath.Join(cwd, "log.txt")},
		{"~/.ssh/id_rsa:raw", filepath.Join(home, ".ssh/id_rsa")},
		{"/etc/hosts", "/etc/hosts"},
		{"report:2024", filepath.Join(cwd, "report:2024")},
		// Not selector grammar: a range may not end in `+`.
		{"a.go:12+", filepath.Join(cwd, "a.go:12+")},
	} {
		t.Run(tc.target, func(t *testing.T) {
			for _, typ := range []string{"tool_call", "tool_approval_requested"} {
				events := ompRuntime.endpointEvents(map[string]interface{}{
					"type": typ, "toolName": "read", "toolCallId": "call-1", "cwd": cwd,
					"input": map[string]interface{}{"path": tc.target},
				}, "sess-1")
				if file := ompFileOf(t, events); file["path"] != tc.want {
					t.Fatalf("%s file.path = %v, want %s", typ, file["path"], tc.want)
				}
			}
		})
	}
}

// Oh My Pi's default edit format can change several files in one call. Its result names no single
// path, so each changed file is recorded as its own file.modified, keeping the edit's operation.
func TestOmpMultiFileEditRecordsEachFile(t *testing.T) {
	events := ompRuntime.endpointEvents(map[string]interface{}{
		"type": "tool_result", "toolName": "edit", "toolCallId": "call-1",
		"input": map[string]interface{}{"input": "[a.go#1A2B]\n…", "paths": []interface{}{"a.go", "b.go"}},
		"details": map[string]interface{}{
			"diff": "a-diff\nb-diff",
			"perFileResults": []interface{}{
				map[string]interface{}{"path": "/repo/a.go", "diff": "a-diff", "op": "update"},
				map[string]interface{}{"path": "/repo/b.go", "diff": "b-diff", "op": "create"},
			},
		},
	}, "sess-1")

	var got [][3]string
	for _, event := range events {
		file, _ := event.fields["file"].(map[string]interface{})
		path, _ := file["path"].(string)
		operation, _ := file["operation"].(string)
		got = append(got, [3]string{event.action, path, operation})
	}
	want := [][3]string{
		{"file.modified", "/repo/a.go", "modify"},
		{"file.modified", "/repo/b.go", "create"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

// `read mcp://<uri>` is an MCP resources/read, recorded as the same mcp.tool_invoked other runtimes
// record for one. The runtime does not report which server served it, so no server is named.
func TestOmpMCPResourceReadIsAnMCPCall(t *testing.T) {
	for typ, action := range map[string]string{"tool_call": "tool.invoked", "tool_result": "mcp.tool_invoked"} {
		events := ompRuntime.endpointEvents(map[string]interface{}{
			"type": typ, "toolName": "read", "toolCallId": "call-1",
			"input": map[string]interface{}{"path": "mcp://ibkr://portfolio/positions"},
			"details": map[string]interface{}{"meta": map[string]interface{}{
				"source": map[string]interface{}{"type": "internal", "value": "mcp://ibkr://portfolio/positions"},
			}},
		}, "sess-1")
		if len(events) != 1 || events[0].action != action {
			t.Fatalf("%s: events = %+v, want one %s", typ, events, action)
		}
		if file, ok := events[0].fields["file"]; ok {
			t.Fatalf("%s grew a file block: %v", typ, file)
		}
		want := map[string]interface{}{
			"method":   map[string]interface{}{"name": "resources/read"},
			"resource": map[string]interface{}{"uri": "ibkr://portfolio/positions"},
		}
		if mcp := events[0].fields["mcp"]; !reflect.DeepEqual(mcp, want) {
			t.Fatalf("%s mcp = %v, want %v", typ, mcp, want)
		}
	}
}
