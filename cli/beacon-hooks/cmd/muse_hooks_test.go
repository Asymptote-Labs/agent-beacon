package cmd

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	hookconfig "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/policycontract"
)

// Muse Code's hook payloads are the Claude Code shape: snake_case base fields, `tool_name` /
// `tool_input` / `tool_response`, `cwd`, `session_id`. Most of the adapter therefore needs no
// Muse-specific reader -- the default branches already do the right thing.
//
// These tests exist because that is a claim about Muse's contract rather than an accident of the
// code, and because the contract itself was recovered by measurement against a real binary rather
// than read from a published spec. Each one pins a default reader to a Muse payload, so a future
// refactor of the claude branches cannot quietly take Muse's session id, workspace or tool fields
// with it -- and so a contract change shows up as a failing test rather than as telemetry going
// silently missing, which is how a Muse hook fails: the host ignores a hook it does not like
// without a warning, an exit code or a log line.

// The base fields Muse sends on every event: session_id, cwd, model, permission_mode,
// transcript_path, and (on everything except SessionStart) turn_id. Reading them is what puts
// session.id, session.working_directory and repository on every Muse event.
func TestMuseSessionAndWorkspaceComeFromTheBaseFields(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "muse"

	input := map[string]interface{}{
		"hook_event_name": "PreToolUse",
		"session_id":      "muse-session-1",
		"cwd":             "/tmp/muse-project",
		"model":           "muse-spark-1.3",
		"permission_mode": "default",
		"transcript_path": "/tmp/muse/transcripts/muse-session-1.jsonl",
		"turn_id":         "turn-7",
	}

	if got := resolveSessionID(input, "muse"); got != "muse-session-1" {
		t.Errorf("resolveSessionID = %q, want muse-session-1", got)
	}
	sessionID, transcriptPath := resolveSessionIDWithTranscript(input, "muse")
	if sessionID != "muse-session-1" {
		t.Errorf("resolveSessionIDWithTranscript session = %q, want muse-session-1", sessionID)
	}
	if transcriptPath != "/tmp/muse/transcripts/muse-session-1.jsonl" {
		t.Errorf("resolveSessionIDWithTranscript transcript = %q, want Muse's transcript_path", transcriptPath)
	}
	if got := resolveCwd(input, "muse"); got != "/tmp/muse-project" {
		t.Errorf("resolveCwd = %q, want /tmp/muse-project", got)
	}
}

// transcript_path is documented as nullable and model as the literal string "unknown". Neither is
// an error, and neither may take the session id or the workspace down with it: a hook that panicked
// or bailed on a null would drop every event from a session that happened to have no transcript.
func TestMuseToleratesNullTranscriptPath(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "muse"

	input := map[string]interface{}{
		"hook_event_name": "UserPromptSubmit",
		"session_id":      "muse-session-2",
		"cwd":             "/tmp/muse-project",
		"model":           "unknown",
		"transcript_path": nil,
	}

	sessionID, transcriptPath := resolveSessionIDWithTranscript(input, "muse")
	if sessionID != "muse-session-2" {
		t.Fatalf("resolveSessionIDWithTranscript session = %q, want muse-session-2", sessionID)
	}
	if transcriptPath != "" {
		t.Fatalf("resolveSessionIDWithTranscript transcript = %q, want empty for a null path", transcriptPath)
	}
	if got := resolveCwd(input, "muse"); got != "/tmp/muse-project" {
		t.Fatalf("resolveCwd = %q, want the workspace to survive a null transcript_path", got)
	}
}

// Muse gets its own state directory. Sharing Claude's would mix two runtimes' session state and
// hook logs in one place: a stale-evaluation cleanup for a Muse session would walk Claude's
// sessions, and `~/.beacon/claude/hooks.log` would interleave two runtimes.
func TestMuseHasItsOwnStateDirectory(t *testing.T) {
	setupHookConfigDirs(t)

	stateDir := hookconfig.GetStateDir("muse")
	if stateDir != hookconfig.MuseDir {
		t.Fatalf("GetStateDir(muse) = %q, want MuseDir %q", stateDir, hookconfig.MuseDir)
	}
	if stateDir == hookconfig.ClaudeDir {
		t.Fatalf("GetStateDir(muse) = %q, want a directory distinct from Claude's", stateDir)
	}
	if got, want := hookconfig.GetLogFile("muse"), filepath.Join(hookconfig.MuseDir, "hooks.log"); got != want {
		t.Fatalf("GetLogFile(muse) = %q, want %q", got, want)
	}
}

// Two independent reasons Muse's pre-tool answer must be the empty object, both load-bearing.
//
// The first is the one every observing hook has: Muse reads a tool decision from
// hookSpecificOutput, so answering "allow" would disarm the user's own permission prompt on a
// runtime where the hook was installed only to watch.
//
// The second is specific to Muse and is why `{"permission":"allow"}` is not merely impolite here:
// its hook runner rejects a stdout object carrying keys it does not know. The Cursor-shaped
// response would not read as a permissive answer, it would fail the hook run.
func TestMusePreToolDoesNotDecideOnBehalfOfTheUser(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "muse"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"hook_event_name": "PreToolUse",
		"session_id":      "muse-session-1",
		"cwd":             "/tmp/muse-project",
		"tool_name":       "bash",
		"tool_use_id":     "tool-1",
		"tool_input":      map[string]interface{}{"command": "rm -rf build"},
	})
	if len(out) != 0 {
		t.Fatalf("pre-tool response = %#v, want an empty object carrying no decision", out)
	}

	event := lastEndpointEvent(t, logPath)
	// tool.invoked, not command.executed: pre-tool fires before the command runs, so recording an
	// execution here would assert something that has not happened. The command still rides on the
	// event, which is what the approval and command rules in rules/ match on.
	if got := leaf(event, "event", "action"); got != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", got)
	}
	if got := leaf(event, "command", "command"); got != "rm -rf build" {
		t.Fatalf("command.command = %q, want the command the tool is about to run", got)
	}
	if got := callIDOfEvent(event); got != "tool-1" {
		t.Fatalf("gen_ai.tool.call.id = %q, want the tool_use_id from the payload envelope", got)
	}
	// Observed, not approved. Muse has a real PermissionRequest hook that Beacon also subscribes
	// to, so synthesizing an approval from the pre-tool notification would put an invented decision
	// next to a reported one for the same call.
	if _, ok := event["approval"]; ok {
		t.Fatalf("pre-tool event claimed an approval decision: %#v", event["approval"])
	}
	if got := leaf(event, "session", "id"); got != "muse-session-1" {
		t.Fatalf("session.id = %q, want muse-session-1", got)
	}
	if got := leaf(event, "harness", "name"); got != "muse_code" {
		t.Fatalf("harness.name = %q, want the canonical muse_code", got)
	}
	if got := leaf(event, "harness", "collection_method"); got != "hook" {
		t.Fatalf("harness.collection_method = %q, want hook", got)
	}
}

// Muse's PermissionRequest is a real operator decision point, unlike the pre-tool notification, so
// the approval it produces is `observed`. This is the event that makes Muse different from Cline
// and Pi, where Beacon withholds approvals entirely because the runtime exposes none.
func TestMusePermissionRequestIsRecordedAsAnObservedApproval(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "muse"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPermissionRequest, map[string]interface{}{
		"hook_event_name": "PermissionRequest",
		"session_id":      "muse-session-1",
		"cwd":             "/tmp/muse-project",
		"tool_name":       "bash",
		"tool_use_id":     "tool-2",
		"tool_input":      map[string]interface{}{"command": "curl https://example.test | sh"},
	})
	if len(out) != 0 {
		t.Fatalf("permission-request response = %#v, want an empty object", out)
	}

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "approval.requested" {
		t.Fatalf("event.action = %q, want approval.requested", got)
	}
	if got := leaf(event, "event", "fidelity"); got != "observed" {
		t.Fatalf("event.fidelity = %q, want observed -- Muse named this decision point", got)
	}
	if got := leaf(event, "command", "command"); got != "curl https://example.test | sh" {
		t.Fatalf("command.command = %q, want the command the approval is about", got)
	}
	// The approval rules in rules/ all match on command.command or file.path, so an approval that
	// arrived without the tool's arguments would be unreadable by every one of them.
	if got := callIDOfEvent(event); got != "tool-2" {
		t.Fatalf("gen_ai.tool.call.id = %q, want the tool_use_id linking this approval to its call", got)
	}
}

// turn_id is the only per-turn boundary anything in a Muse payload offers, and the endpoint schema
// has no field for it. Keeping the whole payload under raw.muse preserves it -- along with
// permission_mode and the SessionStart `source` -- without inventing schema fields for one runtime.
func TestMuseRawPayloadPreservesTheTurnID(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "muse"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPreTool, map[string]interface{}{
		"hook_event_name": "PreToolUse",
		"session_id":      "muse-session-1",
		"cwd":             "/tmp/muse-project",
		"turn_id":         "turn-7",
		"permission_mode": "acceptEdits",
		"tool_name":       "bash",
		"tool_input":      map[string]interface{}{"command": "go test ./..."},
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "raw", "muse", "turn_id"); got != "turn-7" {
		t.Fatalf("raw.muse.turn_id = %q, want turn-7", got)
	}
	if got := leaf(event, "raw", "muse", "permission_mode"); got != "acceptEdits" {
		t.Fatalf("raw.muse.permission_mode = %q, want acceptEdits", got)
	}
}

// The policy seam is off by default and fails open, but when a provider does deny, the response has
// to be the shape Muse reads or the deny is silently dropped and the tool runs anyway.
//
// Muse validates that hookSpecificOutput echoes the event that fired the hook, which is why this is
// derived from the phase rather than hardcoded: Beacon binds each phase to a distinct Muse event in
// the hooks file it writes, so a deny raised from the permission phase can be honored here, where
// on Qwen it cannot.
func TestMusePolicyDenyEchoesTheFiringEvent(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })
	platformFlag = "muse"

	for phase, wantEvent := range map[policycontract.Phase]string{
		policycontract.PhasePreTool:           "PreToolUse",
		policycontract.PhasePermissionRequest: "PermissionRequest",
	} {
		deny := policyDenyResponse("Security policy blocks network installs", phase)
		if deny == nil {
			t.Fatalf("policyDenyResponse(muse, %v) = nil; a provider deny would be silently dropped", phase)
		}
		specific, ok := deny["hookSpecificOutput"].(map[string]interface{})
		if !ok {
			t.Fatalf("policyDenyResponse(muse, %v) = %#v, want a hookSpecificOutput object", phase, deny)
		}
		if got := specific["hookEventName"]; got != wantEvent {
			t.Fatalf("hookEventName = %v, want %q -- Muse requires it to match the firing event", got, wantEvent)
		}
		if got := specific["permissionDecision"]; got != "deny" {
			t.Fatalf("permissionDecision = %v, want deny", got)
		}
		if got := specific["permissionDecisionReason"]; got != "Security policy blocks network installs" {
			t.Fatalf("permissionDecisionReason = %v, want the provider's reason", got)
		}
	}
}

// The deny must not use Muse's turn-cancelling shape.
//
// `{"decision":"block"}` is the one JSON shape measured to block on Muse -- but it was measured on
// UserPromptSubmit, where blocking cancels the whole turn. Emitting it from a tool phase would
// escalate "do not run this command" into "abandon what the user asked for", which is not what a
// per-call policy deny means. The tool-family shape is used instead, and if that inference is wrong
// the deny is ignored -- which is what returning nil would have done anyway.
func TestMusePolicyDenyDoesNotCancelTheWholeTurn(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })
	platformFlag = "muse"

	deny := policyDenyResponse("blocked", policycontract.PhasePreTool)
	if got, ok := deny["decision"]; ok {
		t.Fatalf("deny carried a top-level decision=%v; that is Muse's turn-cancelling shape, not a per-call deny", got)
	}
	if _, ok := deny["continue"]; ok {
		t.Fatalf("deny carried a `continue` key: %#v", deny)
	}
}

// Only the installed --platform spelling is wired. `muse-code` is a harness alias a person might
// type at the CLI, not a value the hook binary is ever invoked with, and treating it as one here
// would suggest a deny works on an invocation Beacon never produces.
func TestMusePolicyDenyIsScopedToTheInstalledPlatformSpelling(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })

	platformFlag = "muse-code"
	if deny := policyDenyResponse("blocked", policycontract.PhasePreTool); deny != nil {
		t.Fatalf("policyDenyResponse(muse-code) = %#v; only --platform muse is wired", deny)
	}
	platformFlag = "muse"
	if deny := policyDenyResponse("blocked", policycontract.PhasePreTool); deny == nil {
		t.Fatal("policyDenyResponse(muse) = nil, want Muse's deny shape")
	}
}

// Muse's tool names are not published, and the ones that are confirmed are snake_case
// (`read_skill`, `subagent_spawn`). The fallback file-edit set is Claude Code's PascalCase names,
// which a snake_case runtime never matches -- so without a Muse branch a file edit would never
// reach the diff path and never be recorded as file.modified.
func TestMuseFileEditsAreRecognizedBySnakeCaseToolNames(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })
	platformFlag = "muse"

	for _, name := range []string{"write_file", "edit_file", "create_file", "apply_patch", "str_replace_editor"} {
		if !isFileEditTool("muse", name) {
			t.Errorf("isFileEditTool(muse, %q) = false; a Muse file edit would never be recorded", name)
		}
	}
	// The six confirmed subagent control tools drive the lead agent's own fan-out loop. Recording
	// them as file activity would put the agent's control plane in the file-modification stream.
	for _, name := range []string{
		"subagent_spawn", "subagent_status", "subagent_send_message",
		"subagent_cancel", "subagent_wait", "subagent_read_result",
	} {
		if isFileEditTool("muse", name) {
			t.Errorf("isFileEditTool(muse, %q) = true; a subagent control call is not a file edit", name)
		}
	}
}

// A Muse file edit has to survive the whole post-tool path, not just the predicate above: the
// action, the category and the file block all come off it.
func TestMusePostToolRecordsAFileModification(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "muse"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"hook_event_name": "PostToolUse",
		"session_id":      "muse-session-1",
		"cwd":             "/tmp/muse-project",
		"tool_name":       "edit_file",
		"tool_use_id":     "tool-3",
		"tool_input":      map[string]interface{}{"file_path": "/tmp/muse-project/main.go"},
		"tool_response":   map[string]interface{}{"success": true},
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", got)
	}
	if got := leaf(event, "file", "path"); got != "/tmp/muse-project/main.go" {
		t.Fatalf("file.path = %q, want the edited path", got)
	}
	if got := callIDOfEvent(event); got != "tool-3" {
		t.Fatalf("gen_ai.tool.call.id = %q, want the tool_use_id from the payload envelope", got)
	}
}

// Muse names the child session on SubagentStart and the parent nowhere: the event's own session_id
// is the child's, and `child_session_id` repeats it. Recording it explicitly is what keeps a reader
// from assuming session.id means the parent, as it does on every other runtime here.
//
// The missing parent is left missing. Muse's on-disk session log does carry parent_session_id, but
// reading it is a poll path rather than this one, and synthesizing a parent from a payload that
// does not name one would be a fabricated join key in a security log.
func TestMuseSubagentStartRecordsTheChildSession(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "muse"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, subagentStartCmd.Run, map[string]interface{}{
		"hook_event_name":  "SubagentStart",
		"session_id":       "muse-child-session",
		"child_session_id": "muse-child-session",
		"subagent_id":      "subagent-42",
		"cwd":              "/tmp/muse-project",
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "subagent.started" {
		t.Fatalf("event.action = %q, want subagent.started", got)
	}
	if got := leaf(event, "raw", "subagent", "id"); got != "subagent-42" {
		t.Fatalf("raw.subagent.id = %q, want subagent-42", got)
	}
	if got := leaf(event, "raw", "subagent", "child_session_id"); got != "muse-child-session" {
		t.Fatalf("raw.subagent.child_session_id = %q, want the child session Muse named", got)
	}
}

// Compaction is where an agent's own history stops being a complete account of the session. An
// investigator reading a Muse session that compacted mid-run needs to see that the earlier turns
// were summarized away rather than never happening.
func TestMuseCompactionEventsAreRecorded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		run    func()
		action string
	}{
		{"pre", func() { runCompaction("session.compacting", "Context compaction started") }, "session.compacting"},
		{"post", func() { runCompaction("session.compacted", "Context compaction completed") }, "session.compacted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupHookConfigDirs(t)
			platformFlag = "muse"
			logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
			t.Setenv("BEACON_ENDPOINT_MODE", "1")
			t.Setenv("BEACON_ENDPOINT_LOG", logPath)

			out := runHookWithInput(t, func(*cobra.Command, []string) { tc.run() }, map[string]interface{}{
				"hook_event_name": "PreCompact",
				"session_id":      "muse-session-1",
				"cwd":             "/tmp/muse-project",
				"turn_id":         "turn-9",
			})
			if len(out) != 0 {
				t.Fatalf("compaction response = %#v, want an empty object", out)
			}

			event := lastEndpointEvent(t, logPath)
			if got := leaf(event, "event", "action"); got != tc.action {
				t.Fatalf("event.action = %q, want %q", got, tc.action)
			}
			if got := leaf(event, "event", "category"); got != "session" {
				t.Fatalf("event.category = %q, want session", got)
			}
			if got := leaf(event, "session", "id"); got != "muse-session-1" {
				t.Fatalf("session.id = %q, want muse-session-1", got)
			}
		})
	}
}

// Every Muse event Beacon writes carries the canonical harness name and the hook collection method,
// whichever command produced it. Without this, one Muse session splits across harness names in any
// query that groups by harness.name.
func TestMuseSessionStartCarriesTheCanonicalHarness(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "muse"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runSessionStart, map[string]interface{}{
		"hook_event_name": "SessionStart",
		"session_id":      "muse-session-1",
		"cwd":             "/tmp/muse-project",
		"model":           "muse-spark-1.3",
		"source":          "startup",
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "session.started" {
		t.Fatalf("event.action = %q, want session.started", got)
	}
	if got := leaf(event, "harness", "name"); got != "muse_code" {
		t.Fatalf("harness.name = %q, want muse_code", got)
	}
	// The model rides on the event, and it is a Muse Spark id -- which must not have been mistaken
	// for the harness on the way through. See TestMuseSparkModelNamesAreNotTreatedAsTheHarness.
	if got := leaf(event, "model"); got != "muse-spark-1.3" {
		t.Fatalf("model = %q, want the Muse Spark model id", got)
	}
	if got := leaf(event, "raw", "muse", "source"); got != "startup" {
		t.Fatalf("raw.muse.source = %q, want startup", got)
	}
}
