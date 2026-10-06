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
	root := t.TempDir()
	for _, tc := range []struct {
		tool, target string
		details      map[string]interface{}
		want         string
	}{
		{"read", ".env:1-20", map[string]interface{}{"meta": map[string]interface{}{
			"source": map[string]interface{}{"type": "path", "value": filepath.Join(root, ".env")},
		}}, filepath.Join(root, ".env")},
		{"write", "notes.md", map[string]interface{}{"resolvedPath": filepath.Join(root, "notes.md")}, filepath.Join(root, "notes.md")},
		{"edit", "main.go", map[string]interface{}{"path": filepath.Join(root, "main.go"), "diff": "-a\n+b"}, filepath.Join(root, "main.go")},
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
	t.Setenv("USERPROFILE", home)
	cwd := t.TempDir()
	// A path that starts at the root is absolute to the runtime. On Windows it names no drive, and
	// lands on the working directory's.
	rooted := func(p string) string { return filepath.VolumeName(cwd) + filepath.FromSlash(p) }
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
		{"/etc/hosts", rooted("/etc/hosts")},
		{"report:2024", filepath.Join(cwd, "report:2024")},
		// Not selector grammar: a range may not end in `+`.
		{"a.go:12+", filepath.Join(cwd, "a.go:12+")},
		// The runtime's shorthand, applied the way its expandPath and resolveToCwd apply it.
		{"/", cwd},
		{"//", cwd},
		{"@/etc/hosts", rooted("/etc/hosts")},
		{"@~/notes.md", filepath.Join(home, "notes.md")},
		{":/etc/hosts", rooted("/etc/hosts")},
		{":../shared/a.go:1-5", filepath.Join(filepath.Dir(cwd), "shared/a.go")},
		{"~work/notes.md", filepath.Join(home, "work/notes.md")},
		{"Screenshot 2026-10-05 at 3.35.12\u202fPM.png", filepath.Join(cwd, "Screenshot 2026-10-05 at 3.35.12 PM.png")},
		// A leading `@` before anything else is part of the name.
		{"@notes.md", filepath.Join(cwd, "@notes.md")},
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

// A read the runtime reports as a URL or one of its own resources is not a file read, even when a
// file in the runtime's storage backs it and the spelling looked like a path: the runtime routes
// `local:/plan.md` as `local://plan.md`. And a URL behind an `@` mention marker is a URL, not a
// file named `@skill:/…`; the runtime refuses to read it as a path at all.
func TestOmpResourceReadIsNotAFileWhateverItsSpelling(t *testing.T) {
	for _, payload := range []map[string]interface{}{
		{"type": "tool_result", "toolName": "read", "input": map[string]interface{}{"path": "local:/plan.md"},
			"details": map[string]interface{}{
				"resolvedPath": "/home/u/.omp/agent/sessions/s/local/plan.md",
				"meta":         map[string]interface{}{"source": map[string]interface{}{"type": "internal", "value": "local://plan.md"}},
			}},
		{"type": "tool_call", "toolName": "read", "input": map[string]interface{}{"path": "@skill://self-verify-beacon-in-sandbox"}},
		{"type": "tool_result", "toolName": "read", "input": map[string]interface{}{"path": "@skill://self-verify-beacon-in-sandbox"},
			"isError": true},
	} {
		payload["toolCallId"], payload["cwd"] = "call-1", "/repo"
		events := ompRuntime.endpointEvents(payload, "sess-1")
		if len(events) != 1 {
			t.Fatalf("%v produced %d events, want 1", payload, len(events))
		}
		if file, ok := events[0].fields["file"]; ok {
			t.Fatalf("%s of %v grew a file block: %v", payload["type"], payload["input"], file)
		}
	}
}

func TestOmpDriveAliasPathFollowsTheHost(t *testing.T) {
	for _, tc := range []struct {
		path, goos string
		wsl        bool
		want       string
	}{
		{"/c/Users/me/a.go", "windows", false, `C:\Users\me\a.go`},
		{"/mnt/d/src/a.go", "windows", false, `D:\src\a.go`},
		{"/c", "windows", false, `C:\`},
		{"/home/me/a.go", "windows", false, "/home/me/a.go"},
		{`C:\Users\me\a.go`, "linux", true, "/mnt/c/Users/me/a.go"},
		{"C:/Users/me/a.go", "linux", true, "/mnt/c/Users/me/a.go"},
		{`C:\Users\me\a.go`, "linux", false, `C:\Users\me\a.go`},
		{"/c/Users/me/a.go", "darwin", false, "/c/Users/me/a.go"},
		// `..` stops at the drive root, as path.win32.normalize stops it, rather than climbing
		// out of the drive's mount.
		{`C:\..\Windows\a.go`, "linux", true, "/mnt/c/Windows/a.go"},
		{`C:\a\.\b\..\c.go`, "linux", true, "/mnt/c/a/c.go"},
		{`C:\`, "linux", true, "/mnt/c"},
	} {
		if got := ompDriveAliasPath(tc.path, tc.goos, tc.wsl); got != tc.want {
			t.Errorf("ompDriveAliasPath(%q, %s, wsl=%v) = %q, want %q", tc.path, tc.goos, tc.wsl, got, tc.want)
		}
	}
}

// A `file://` URL names the path Node's url.fileURLToPath gives it on the host, which is how these
// runtimes read one. On Windows that is a drive or UNC path, not the URL's `/C:/...` path, which is
// not absolute there.
func TestFileURLPathFollowsTheHost(t *testing.T) {
	for _, tc := range []struct{ url, goos, want string }{
		{"file:///C:/Users/me/a.go", "windows", `C:\Users\me\a.go`},
		{"file:///c:/my%20docs/a.go", "windows", `c:\my docs\a.go`},
		{"file://localhost/C:/a.go", "windows", `C:\a.go`},
		{"file://C:/a.go", "windows", `C:\a.go`},
		{"file://server/share/a.go", "windows", `\\server\share\a.go`},
		{"file:///Users/me/a.go", "windows", ""},
		{"file:///C:/a%5Cb.go", "windows", ""},
		{"file:///repo/main.go", "darwin", "/repo/main.go"},
		{"file://localhost/repo/main.go", "linux", "/repo/main.go"},
		{"file:///repo/my%20notes.md", "linux", "/repo/my notes.md"},
		{"file://server/share/a.go", "linux", ""},
		{"file:///repo/a%2Fb.go", "linux", ""},
	} {
		if got := fileURLPath(tc.url, tc.goos); got != tc.want {
			t.Errorf("fileURLPath(%q, %s) = %q, want %q", tc.url, tc.goos, got, tc.want)
		}
	}
}

// Oh My Pi's default edit format can change several files in one call. Its result names no single
// path, so each changed file is recorded as its own file.modified, keeping the edit's operation.
func TestOmpMultiFileEditRecordsEachFile(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a.go"), filepath.Join(root, "b.go")
	events := ompRuntime.endpointEvents(map[string]interface{}{
		"type": "tool_result", "toolName": "edit", "toolCallId": "call-1",
		"input": map[string]interface{}{"input": "[a.go#1A2B]\n…", "paths": []interface{}{"a.go", "b.go"}},
		"details": map[string]interface{}{
			"diff": "a-diff\nb-diff",
			"perFileResults": []interface{}{
				map[string]interface{}{"path": a, "diff": "a-diff", "op": "update"},
				map[string]interface{}{"path": b, "diff": "b-diff", "op": "create"},
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
		{"file.modified", a, "modify"},
		{"file.modified", b, "create"},
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
