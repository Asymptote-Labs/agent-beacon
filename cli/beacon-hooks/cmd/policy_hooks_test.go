package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/mdr"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/transcript"
)

const p45Command = "/opt/homebrew/bin/pnpm config get '//registry.tiptap.dev/:_authToken'"

// A 64-character hex token, the shape of the real Tiptap token in P-45.
var canaryTiptapToken = strings.Repeat("9f3a1c7e5b2d8046", 4)

type fakeJudge struct {
	mu       sync.Mutex
	requests []mdr.Request
	feedback []mdr.Feedback
	response string
	status   int
}

func (f *fakeJudge) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/feedback") {
			var fb mdr.Feedback
			_ = json.NewDecoder(r.Body).Decode(&fb)
			f.mu.Lock()
			f.feedback = append(f.feedback, fb)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		var req mdr.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.requests = append(f.requests, req)
		f.mu.Unlock()
		if f.status != 0 {
			w.WriteHeader(f.status)
		}
		_, _ = w.Write([]byte(f.response))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setupPolicyHookTest(t *testing.T, judge *fakeJudge) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_POLICY_STATE_DIR", t.TempDir())
	t.Setenv("BEACON_POLICY_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv("BEACON_POLICY_PREFILTER", "")
	t.Setenv("HOME", t.TempDir())
	if judge != nil {
		t.Setenv(mdr.URLEnv, judge.serve(t).URL+"/v1/mdr/decide")
		t.Setenv(mdr.TokenEnv, "ask_live_test")
	} else {
		t.Setenv(mdr.URLEnv, "")
	}
	return logPath
}

func writeTranscript(t *testing.T, prompts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	var b strings.Builder
	for _, p := range prompts {
		raw, _ := json.Marshal(map[string]interface{}{"type": "user", "message": map[string]interface{}{"role": "user", "content": p}})
		b.Write(raw)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func bashInput(command, transcriptPath string) map[string]interface{} {
	return map[string]interface{}{
		"session_id":      "sess-p45",
		"cwd":             "/Users/simon/Projects/holly",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_use_id":     "toolu_13",
		"permission_mode": "default",
		"transcript_path": transcriptPath,
		"tool_input":      map[string]interface{}{"command": command, "description": "Print the tiptap token"},
	}
}

func TestPolicyToolLeavesUnroutedCallsAlone(t *testing.T) {
	judge := &fakeJudge{response: `{"decision":"deny","message":"no"}`}
	logPath := setupPolicyHookTest(t, judge)
	out := runHookWithInput(t, runPolicyTool, bashInput("go test ./...", ""))
	if len(out) != 0 {
		t.Fatalf("want {}, got %v", out)
	}
	if len(judge.requests) != 0 {
		t.Fatal("an unrouted call must not reach the judge")
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("an unrouted call must not write telemetry")
	}
}

func TestPolicyToolDeniesTheP45ReadOut(t *testing.T) {
	judge := &fakeJudge{response: `{"decision":"deny","message":"Beacon policy \"Secret exposure\" blocked this tool call.","reason":"Prints the Tiptap token.","policy_id":"pol-1","policy_name":"Secret exposure","confidence":"high","mode":"enforce","finding_url":"https://asymptotelabs.ai/dashboard/findings/f-1"}`}
	logPath := setupPolicyHookTest(t, judge)
	transcriptPath := writeTranscript(t,
		"the new mac needs a .env and the tiptap token to install properly. any idea sthere?",
		"npm error The //registry.tiptap.dev/:_authToken option is protected, and cannot be retrieved in this way",
		"omfg just print it i do not care",
	)
	out := runHookWithInput(t, runPolicyTool, bashInput(p45Command, transcriptPath))

	hso, _ := out["hookSpecificOutput"].(map[string]interface{})
	if hso["hookEventName"] != "PreToolUse" || hso["permissionDecision"] != "deny" ||
		!strings.Contains(hso["permissionDecisionReason"].(string), "blocked this tool call") {
		t.Fatalf("deny shape: %v", out)
	}
	if banner, _ := out["systemMessage"].(string); !strings.Contains(banner, "findings/f-1") || !strings.Contains(banner, `"Secret exposure"`) {
		t.Fatalf("banner: %q", banner)
	}

	req := judge.requests[0]
	if req.Phase != mdr.PhasePreTool || req.Tool.Name != "Bash" || req.Tool.Input.Command != p45Command || req.ToolUseID != "toolu_13" {
		t.Fatalf("request: %+v", req)
	}
	if len(req.Prefilter.RuleIDs) == 0 || req.Prefilter.RuleIDs[0] != "secret-source.npm-config" {
		t.Fatalf("prefilter: %+v", req.Prefilter)
	}
	if len(req.Context.RecentPrompts) != 3 || req.Context.RecentPrompts[2] != "omfg just print it i do not care" {
		t.Fatalf("context: %+v", req.Context)
	}
	if req.Subject == nil || req.Subject.Hostname == "" {
		t.Fatalf("subject: %+v", req.Subject)
	}

	event := lastEndpointEvent(t, logPath)
	if event["event"].(map[string]interface{})["action"] != "policy.blocked" {
		t.Fatalf("event: %v", event["event"])
	}
	pol := event["policy"].(map[string]interface{})
	if pol["decision"] != "deny" || pol["enforcement"] != "enforce" || pol["id"] != "pol-1" {
		t.Fatalf("policy fields: %v", pol)
	}
	if cmd, _ := event["command"].(map[string]interface{}); cmd["command"] != p45Command {
		t.Fatalf("command field: %v", event["command"])
	}

	// A second attempt carries the earlier denial as context for the judge.
	runHookWithInput(t, runPolicyTool, bashInput("~/Library/pnpm/pnpm config get '//registry.tiptap.dev/:_authToken'", transcriptPath))
	prior := judge.requests[1].Context.PriorDecisions
	if len(prior) != 1 || !strings.HasPrefix(prior[0], "deny Bash `/opt/homebrew/bin/pnpm") {
		t.Fatalf("prior decisions: %q", prior)
	}
}

func TestPolicyToolAsks(t *testing.T) {
	judge := &fakeJudge{response: `{"decision":"ask","message":"Beacon policy flagged this call: a branch prints the token.","policy_id":"pol-1","mode":"enforce"}`}
	logPath := setupPolicyHookTest(t, judge)
	out := runHookWithInput(t, runPolicyTool, bashInput(p45Command, ""))
	hso, _ := out["hookSpecificOutput"].(map[string]interface{})
	if hso["permissionDecision"] != "ask" || !strings.Contains(hso["permissionDecisionReason"].(string), "branch prints") {
		t.Fatalf("ask shape: %v", out)
	}
	if lastEndpointEvent(t, logPath)["event"].(map[string]interface{})["action"] != "policy.asked" {
		t.Fatal("want policy.asked")
	}
}

func TestPolicyToolShadowAllowsAndRecords(t *testing.T) {
	judge := &fakeJudge{response: `{"decision":"allow","mode":"monitor","policy_id":"pol-1","reason":"would print the token"}`}
	logPath := setupPolicyHookTest(t, judge)
	out := runHookWithInput(t, runPolicyTool, bashInput(p45Command, ""))
	if len(out) != 0 {
		t.Fatalf("shadow must be silent, got %v", out)
	}
	event := lastEndpointEvent(t, logPath)
	if event["event"].(map[string]interface{})["action"] != "policy.flagged" || event["policy"].(map[string]interface{})["enforcement"] != "monitor" {
		t.Fatalf("event: %v %v", event["event"], event["policy"])
	}
}

func TestPolicyToolFailsOpenAndSaysSo(t *testing.T) {
	for name, judge := range map[string]*fakeJudge{
		"server error": {status: 500, response: `{}`},
		"garbage":      {response: `not json`},
	} {
		t.Run(name, func(t *testing.T) {
			logPath := setupPolicyHookTest(t, judge)
			if out := runHookWithInput(t, runPolicyTool, bashInput(p45Command, "")); len(out) != 0 {
				t.Fatalf("must allow, got %v", out)
			}
			if lastEndpointEvent(t, logPath)["event"].(map[string]interface{})["action"] != "policy.unavailable" {
				t.Fatal("want policy.unavailable")
			}
		})
	}
	t.Run("not configured", func(t *testing.T) {
		logPath := setupPolicyHookTest(t, nil)
		if out := runHookWithInput(t, runPolicyTool, bashInput(p45Command, "")); len(out) != 0 {
			t.Fatalf("must allow, got %v", out)
		}
		if lastEndpointEvent(t, logPath)["event"].(map[string]interface{})["action"] != "policy.unavailable" {
			t.Fatal("want policy.unavailable")
		}
	})
}

func TestPolicyToolMasksSecretsBeforeSending(t *testing.T) {
	judge := &fakeJudge{response: `{"decision":"allow"}`}
	setupPolicyHookTest(t, judge)
	transcriptPath := writeTranscript(t, "here it is TIPTAP_PRO_TOKEN="+canaryTiptapToken)
	runHookWithInput(t, runPolicyTool, bashInput("TIPTAP_PRO_TOKEN="+canaryTiptapToken+" pnpm install", transcriptPath))
	raw, _ := json.Marshal(judge.requests[0])
	if strings.Contains(string(raw), canaryTiptapToken) {
		t.Fatalf("secret sent to the judge: %s", raw)
	}
	if !strings.Contains(judge.requests[0].Tool.Input.Command, "TIPTAP_PRO_TOKEN=…") {
		t.Fatalf("command not masked as expected: %q", judge.requests[0].Tool.Input.Command)
	}
}

func TestPolicyToolRoutesCredentialFileReads(t *testing.T) {
	judge := &fakeJudge{response: `{"decision":"deny","message":"blocked"}`}
	setupPolicyHookTest(t, judge)
	out := runHookWithInput(t, runPolicyTool, map[string]interface{}{
		"session_id": "s", "tool_name": "Read", "tool_input": map[string]interface{}{"file_path": "/Users/zac/Projects/holly/.env"},
	})
	if out["hookSpecificOutput"] == nil || judge.requests[0].Tool.Input.FilePath != "/Users/zac/Projects/holly/.env" {
		t.Fatalf("out=%v req=%+v", out, judge.requests)
	}
}

func TestPolicyToolSendsTheGrepOutputMode(t *testing.T) {
	// The judge needs it: content prints matching lines, the default prints file names.
	judge := &fakeJudge{response: `{"decision":"allow"}`}
	setupPolicyHookTest(t, judge)
	runHookWithInput(t, runPolicyTool, map[string]interface{}{
		"session_id": "s", "tool_name": "Grep",
		"tool_input": map[string]interface{}{"pattern": "API_KEY", "path": "/Users/zac/Projects/holly/.env.local", "output_mode": "content"},
	})
	if len(judge.requests) != 1 || judge.requests[0].Tool.Input.OutputMode != "content" {
		t.Fatalf("req=%+v", judge.requests)
	}
}

func TestPolicyToolSendsEveryMCPArgument(t *testing.T) {
	// A secret in a header must reach the judge (masked) even though the call
	// also carries a url, which alone used to be all the judge saw.
	judge := &fakeJudge{response: `{"decision":"allow"}`}
	setupPolicyHookTest(t, judge)
	token := "ghp_" + "aZ3kQ9mX2pL7vR4tB8nC1wE6yU5sD0fGh2Jk"
	runHookWithInput(t, runPolicyTool, map[string]interface{}{
		"session_id": "s", "tool_name": "mcp__http__request",
		"tool_input": map[string]interface{}{
			"url":     "https://api.github.com/user",
			"headers": map[string]interface{}{"Authorization": "token " + token},
		},
	})
	if len(judge.requests) != 1 {
		t.Fatalf("the MCP call was not judged: %+v", judge.requests)
	}
	in := judge.requests[0].Tool.Input
	if !strings.Contains(in.Command, "headers") || !strings.Contains(in.Command, "api.github.com") {
		t.Fatalf("the judge did not see every argument: %+v", in)
	}
	if strings.Contains(in.Command, token) {
		t.Fatalf("the token reached the judge unmasked: %s", in.Command)
	}
}

func TestPolicyToolIgnoresOtherPlatforms(t *testing.T) {
	judge := &fakeJudge{response: `{"decision":"deny","message":"blocked"}`}
	setupPolicyHookTest(t, judge)
	platformFlag = "cursor"
	defer func() { platformFlag = "claude" }()
	runHookWithInput(t, runPolicyTool, bashInput(p45Command, ""))
	if len(judge.requests) != 0 {
		t.Fatal("v0 is Claude Code only")
	}
}

func TestPolicyPromptBlocksASecretLocally(t *testing.T) {
	judge := &fakeJudge{response: `{"decision":"allow"}`}
	logPath := setupPolicyHookTest(t, judge)
	prompt := "the new mac needs this, TIPTAP_PRO_TOKEN=" + canaryTiptapToken + " can you add it"
	out := runHookWithInput(t, runPolicyPrompt, map[string]interface{}{"session_id": "sess-p", "prompt": prompt})
	if out["decision"] != "block" || !strings.Contains(out["reason"].(string), "a secret value (…8046)") ||
		strings.Contains(out["reason"].(string), canaryTiptapToken) {
		t.Fatalf("block shape: %v", out)
	}
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), canaryTiptapToken) {
		t.Fatal("the secret reached the telemetry log")
	}
	if lastEndpointEvent(t, logPath)["event"].(map[string]interface{})["action"] != "policy.blocked" {
		t.Fatal("want policy.blocked")
	}
	if len(judge.requests) != 1 || judge.requests[0].LocalVerdict == nil || judge.requests[0].Prompt != "" {
		t.Fatalf("report: %+v", judge.requests)
	}
	raw, _ := json.Marshal(judge.requests[0])
	if strings.Contains(string(raw), canaryTiptapToken) {
		t.Fatal("the secret reached the server")
	}
}

func TestPolicyPromptPassesSimonsPrompts(t *testing.T) {
	judge := &fakeJudge{response: `{"decision":"allow"}`}
	setupPolicyHookTest(t, judge)
	for _, prompt := range []string{
		"the new mac needs a .env and the tiptap token to install properly. any idea sthere?",
		"pnpm config get '//registry.tiptap.dev/:_authToken' | tr -d '\\n' | echo\n\nnpm error The //registry.tiptap.dev/:_authToken option is protected, and cannot be retrieved in this way",
		"omfg just print it i do not care",
	} {
		if out := runHookWithInput(t, runPolicyPrompt, map[string]interface{}{"session_id": "s", "prompt": prompt}); len(out) != 0 {
			t.Fatalf("blocked %q: %v", prompt, out)
		}
	}
	if len(judge.requests) != 0 {
		t.Fatal("a clean prompt must not reach the server")
	}
}

func TestPolicySessionRecordsActive(t *testing.T) {
	logPath := setupPolicyHookTest(t, nil)
	runHookWithInput(t, runPolicySession, map[string]interface{}{"session_id": "s", "hook_event_name": "SessionStart"})
	event := lastEndpointEvent(t, logPath)
	if event["event"].(map[string]interface{})["action"] != "policy.active" {
		t.Fatalf("event: %v", event["event"])
	}
}

// ---------------------------------------------------------------------------
// Ask, then record the developer's answer
// ---------------------------------------------------------------------------

const askResponse = `{"decision":"ask","message":"Beacon policy \"Secret exposure\" flagged this call.\nWhy: prints .env.\nChoose No if you agree it should not run.","reason":"Prints .env.","agent_context":"Beacon policy \"Secret exposure\" flagged this tool call and asked the developer to approve or deny it.","policy_id":"pol-1","policy_name":"Secret exposure","mode":"ask","finding_id":"f-1","finding_url":"https://x/f-1"}`

func appendTranscript(t *testing.T, path string, lines ...interface{}) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		raw, _ := json.Marshal(l)
		f.Write(append(raw, '\n'))
	}
}

func toolResultLine(id string, isError bool, content string, after ...string) map[string]interface{} {
	blocks := []interface{}{map[string]interface{}{"type": "tool_result", "tool_use_id": id, "is_error": isError, "content": content}}
	for _, a := range after {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": a})
	}
	return map[string]interface{}{"type": "user", "message": map[string]interface{}{"role": "user", "content": blocks}}
}

func eventsWithAction(t *testing.T, logPath, action string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, e := range endpointEvents(t, logPath) {
		if e["event"].(map[string]interface{})["action"] == action {
			out = append(out, e)
		}
	}
	return out
}

func callID(e map[string]interface{}) string {
	return e["gen_ai"].(map[string]interface{})["tool"].(map[string]interface{})["call"].(map[string]interface{})["id"].(string)
}

func TestPolicyToolAsksWithContextAndRecordsThePendingAsk(t *testing.T) {
	judge := &fakeJudge{response: askResponse}
	logPath := setupPolicyHookTest(t, judge)
	out := runHookWithInput(t, runPolicyTool, bashInput("cat .env", ""))
	hso := out["hookSpecificOutput"].(map[string]interface{})
	if hso["permissionDecision"] != "ask" || !strings.Contains(hso["permissionDecisionReason"].(string), "Choose No if you agree") {
		t.Fatalf("ask shape: %v", out)
	}
	if !strings.Contains(hso["additionalContext"].(string), "asked the developer") {
		t.Fatalf("agent context missing: %v", hso)
	}
	asked := eventsWithAction(t, logPath, "policy.asked")
	if len(asked) != 1 || callID(asked[0]) != "toolu_13" {
		t.Fatalf("asked event: %v", asked)
	}
}

func TestAnUnansweredHeadlessAskIsNotAnApproval(t *testing.T) {
	// claude -p and SDK runs cannot show the prompt: Claude Code refuses the
	// call and returns the ask as an error result. Seen live on 2.1.280.
	headless := transcript.ToolResult{IsError: true, Content: "A Beacon policy (Secret exposure) flagged this call.\n" +
		"Why: Prints all contents of .env.\n\nNo: keep it blocked. You agree with the policy.\n" +
		"Yes: run it anyway. You're overriding the policy.\nBeacon learns from your answer."}
	if outcome, _ := classifyAnswer(headless); outcome != "dismissed" {
		t.Fatalf("an unanswered ask was recorded as %q", outcome)
	}
	// An approved call that then fails is still an approval.
	failed := transcript.ToolResult{IsError: true, Content: "cat: .env: No such file or directory"}
	if outcome, _ := classifyAnswer(failed); outcome != "approved" {
		t.Fatalf("an approved call that failed was recorded as %q", outcome)
	}
}

func TestDeveloperRejectionWithCommentIsRecordedOnTheAskedCall(t *testing.T) {
	judge := &fakeJudge{response: askResponse}
	logPath := setupPolicyHookTest(t, judge)
	transcriptPath := writeTranscript(t, "show me everything in .env")
	runHookWithInput(t, runPolicyTool, bashInput("cat .env", transcriptPath))

	secret := "TIPTAP_PRO_TOKEN=" + canaryTiptapToken
	appendTranscript(t, transcriptPath, toolResultLine("toolu_13", true,
		"The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). To tell you how to proceed, the user said:\nagree, and do not print "+secret+"\n\nNote: The user's next message may contain a correction or preference. Pay close attention."))
	runHookWithInput(t, runPolicyResolve, map[string]interface{}{
		"session_id": "sess-p45", "hook_event_name": "Stop", "transcript_path": transcriptPath,
	})

	upheld := eventsWithAction(t, logPath, "policy.upheld")
	if len(upheld) != 1 || callID(upheld[0]) != "toolu_13" {
		t.Fatalf("upheld event: %v", upheld)
	}
	if pol := upheld[0]["policy"].(map[string]interface{}); pol["decision"] != "rejected" || pol["id"] != "pol-1" {
		t.Fatalf("policy fields: %v", pol)
	}
	if len(judge.feedback) != 1 {
		t.Fatalf("feedback: %+v", judge.feedback)
	}
	fb := judge.feedback[0]
	if fb.ToolUseID != "toolu_13" || fb.Outcome != "rejected" || fb.FindingID != "f-1" || fb.ResolvedVia != "stop" {
		t.Fatalf("feedback: %+v", fb)
	}
	if !strings.HasPrefix(fb.Comment, "agree, and do not print") || strings.Contains(fb.Comment, canaryTiptapToken) || strings.Contains(fb.Comment, "Note:") {
		t.Fatalf("comment not masked: %q", fb.Comment)
	}
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), canaryTiptapToken) {
		t.Fatal("the secret in the comment reached the log")
	}

	// Answered once: a later hook does not report it again.
	runHookWithInput(t, runPolicyResolve, map[string]interface{}{"session_id": "sess-p45", "hook_event_name": "SessionEnd", "transcript_path": transcriptPath})
	if len(judge.feedback) != 1 {
		t.Fatal("an answer must be reported once")
	}
}

func TestApprovalIsResolvedByTheNextToolCallAndKeepsItsOwnCallID(t *testing.T) {
	judge := &fakeJudge{response: askResponse}
	logPath := setupPolicyHookTest(t, judge)
	transcriptPath := writeTranscript(t, "show me everything in .env")
	runHookWithInput(t, runPolicyTool, bashInput("cat .env", transcriptPath))
	appendTranscript(t, transcriptPath, toolResultLine("toolu_13", false, "PORT=3022", "dev values only, fine"))

	next := bashInput("go test ./...", transcriptPath)
	next["tool_use_id"] = "toolu_14"
	runHookWithInput(t, runPolicyTool, next)

	overridden := eventsWithAction(t, logPath, "policy.overridden")
	if len(overridden) != 1 || callID(overridden[0]) != "toolu_13" {
		t.Fatalf("overridden event must carry the asked call's id: %v", overridden)
	}
	if judge.feedback[0].Outcome != "approved" || judge.feedback[0].Comment != "dev values only, fine" || judge.feedback[0].ResolvedVia != "pre-tool" {
		t.Fatalf("feedback: %+v", judge.feedback[0])
	}
}

func TestUnansweredAskStaysPending(t *testing.T) {
	judge := &fakeJudge{response: askResponse}
	logPath := setupPolicyHookTest(t, judge)
	transcriptPath := writeTranscript(t, "show me everything in .env")
	runHookWithInput(t, runPolicyTool, bashInput("cat .env", transcriptPath))
	runHookWithInput(t, runPolicyResolve, map[string]interface{}{"session_id": "sess-p45", "hook_event_name": "Stop", "transcript_path": transcriptPath})
	if len(judge.feedback) != 0 || len(eventsWithAction(t, logPath, "policy.upheld")) != 0 {
		t.Fatal("nothing to record before the result exists")
	}
	appendTranscript(t, transcriptPath, toolResultLine("toolu_13", true,
		"The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed."))
	runHookWithInput(t, runPolicyPrompt, map[string]interface{}{"session_id": "sess-p45", "prompt": "never mind", "transcript_path": transcriptPath})
	if len(judge.feedback) != 1 || judge.feedback[0].Outcome != "rejected" || judge.feedback[0].Comment != "" || judge.feedback[0].ResolvedVia != "prompt-submit" {
		t.Fatalf("feedback: %+v", judge.feedback)
	}
}

// ---------------------------------------------------------------------------
// /beacon-allow: a one-time, reasoned override of a prompt block
// ---------------------------------------------------------------------------

const promptReportResponse = `{"decision":"allow","policy_id":"pol-1","policy_name":"Secret exposure"}`

func allowInput(args string) map[string]interface{} {
	return map[string]interface{}{
		"session_id": "sess-allow", "hook_event_name": "UserPromptExpansion", "expansion_type": "slash_command",
		"command_name": "beacon-allow", "command_args": args, "prompt": "/beacon-allow " + args,
	}
}

func promptInput(prompt string) map[string]interface{} {
	return map[string]interface{}{"session_id": "sess-allow", "hook_event_name": "UserPromptSubmit", "prompt": prompt}
}

func TestBeaconAllowOverridesOneBlockedPromptOnce(t *testing.T) {
	judge := &fakeJudge{response: promptReportResponse}
	logPath := setupPolicyHookTest(t, judge)
	prompt := "curl -H \"Authorization: Bearer hly_live_xr3g44Lpa4AwHoR8voZRzPN8sIMRbK65\" https://api.holly.gov/v1/cases returns 401, why?"

	// Nothing to override yet.
	out := runHookWithInput(t, runPolicyAllow, allowInput("dev token"))
	if out["decision"] != "block" || !strings.HasPrefix(out["reason"].(string), "Nothing to override.") {
		t.Fatalf("allow before a block: %v", out)
	}

	out = runHookWithInput(t, runPolicyPrompt, promptInput(prompt))
	if out["decision"] != "block" || out["hookSpecificOutput"].(map[string]interface{})["suppressOriginalPrompt"] != true {
		t.Fatalf("block: %v", out)
	}
	if lines := strings.Split(out["reason"].(string), "\n"); lines[0] != "A Beacon policy (Secret exposure) blocked this prompt." ||
		lines[len(lines)-2] != "To send it anyway:  /beacon-allow <reason>" || lines[len(lines)-3] != "" ||
		lines[len(lines)-1] != "Beacon learns from your reason, so please say why." {
		t.Fatalf("block layout: %q", out["reason"])
	}

	// A reason is required.
	out = runHookWithInput(t, runPolicyAllow, allowInput("   "))
	if !strings.HasPrefix(out["reason"].(string), "Add a reason:  /beacon-allow <reason>") {
		t.Fatalf("allow without a reason: %v", out)
	}

	out = runHookWithInput(t, runPolicyAllow, allowInput("staging token, rotating it after"))
	if out["decision"] != "block" || out["hookSpecificOutput"].(map[string]interface{})["suppressOriginalPrompt"] != true ||
		out["reason"] != "Override recorded: \"staging token, rotating it after\"\nResend the blocked prompt within 10 minutes. It will go through once." {
		t.Fatalf("allow: %q", out["reason"])
	}
	if len(judge.feedback) != 1 {
		t.Fatalf("feedback: %+v", judge.feedback)
	}
	fb := judge.feedback[0]
	if !strings.HasPrefix(fb.ToolUseID, "prompt:") || fb.Outcome != "approved" || fb.Comment != "staging token, rotating it after" ||
		fb.PolicyID != "pol-1" || fb.ResolvedVia != "beacon-allow" {
		t.Fatalf("feedback: %+v", fb)
	}
	overridden := eventsWithAction(t, logPath, "policy.overridden")
	if len(overridden) != 1 || callID(overridden[0]) != fb.ToolUseID {
		t.Fatalf("overridden event must carry the block's id: %v", overridden)
	}
	if blocked := eventsWithAction(t, logPath, "policy.blocked"); callID(blocked[0]) != fb.ToolUseID {
		t.Fatal("the block and the override must share an id")
	}

	// The resend goes through once; a second resend is blocked again.
	if out := runHookWithInput(t, runPolicyPrompt, promptInput(prompt)); len(out) != 0 {
		t.Fatalf("resend under the override must pass: %v", out)
	}
	if used := eventsWithAction(t, logPath, "policy.override_used"); len(used) != 1 || callID(used[0]) != fb.ToolUseID {
		t.Fatalf("override_used: %v", used)
	}
	if out := runHookWithInput(t, runPolicyPrompt, promptInput(prompt)); out["decision"] != "block" {
		t.Fatalf("an override is used once: %v", out)
	}
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), "hly_live_xr3g44Lpa4AwHoR8voZRzPN8sIMRbK65") {
		t.Fatal("the secret reached the log")
	}
}

func TestBeaconAllowDoesNotCoverADifferentSecret(t *testing.T) {
	judge := &fakeJudge{response: promptReportResponse}
	setupPolicyHookTest(t, judge)
	runHookWithInput(t, runPolicyPrompt, promptInput("use TIPTAP_PRO_TOKEN="+canaryTiptapToken))
	runHookWithInput(t, runPolicyAllow, allowInput("dev token"))
	other := "ghp_" + "aZ3kQ9mX2pL7vR4tB8nC1wE6yU5sD0fGh2Jk"
	if out := runHookWithInput(t, runPolicyPrompt, promptInput("use TIPTAP_PRO_TOKEN="+canaryTiptapToken+" and "+other)); out["decision"] != "block" {
		t.Fatalf("a prompt with a second, unallowed secret must be blocked: %v", out)
	}
}

func TestPolicyAllowIgnoresOtherCommands(t *testing.T) {
	setupPolicyHookTest(t, &fakeJudge{response: promptReportResponse})
	in := allowInput("x")
	in["command_name"] = "deploy"
	if out := runHookWithInput(t, runPolicyAllow, in); len(out) != 0 {
		t.Fatalf("other commands must pass: %v", out)
	}
}
