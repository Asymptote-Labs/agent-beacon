package policystate

import (
	"fmt"
	"testing"
	"time"
)

func TestAppendKeepsTheMostRecent(t *testing.T) {
	t.Setenv("BEACON_POLICY_STATE_DIR", t.TempDir())
	for i := 0; i < maxEntries+2; i++ {
		if err := Append("sess/../1", Entry{At: time.Now(), Decision: "deny", Target: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	got := Load("sess/../1")
	if len(got) != maxEntries || got[0].Target != "2" || got[maxEntries-1].Target != fmt.Sprint(maxEntries+1) {
		t.Fatalf("got %d entries, first %q", len(got), got[0].Target)
	}
	if Load("other") != nil {
		t.Fatal("sessions must not share state")
	}
}

func TestPendingAsksAndAnswers(t *testing.T) {
	t.Setenv("BEACON_POLICY_STATE_DIR", t.TempDir())
	_ = Append("s", Entry{Decision: "ask", ToolUseID: "t1"})
	_ = Append("s", Entry{Decision: "deny", ToolUseID: "t2"})
	_ = Append("s", Entry{Decision: "ask", ToolUseID: "t3"})
	if p := Pending("s"); len(p) != 2 || p[0].ToolUseID != "t1" || p[1].ToolUseID != "t3" {
		t.Fatalf("pending: %+v", p)
	}
	if err := Answer("s", "t1", "rejected", "agree", time.Now()); err != nil {
		t.Fatal(err)
	}
	if p := Pending("s"); len(p) != 1 || p[0].ToolUseID != "t3" {
		t.Fatalf("after answer: %+v", p)
	}
	all := Load("s")
	if all[0].Outcome != "rejected" || all[0].Comment != "agree" || all[0].AnsweredAt.IsZero() {
		t.Fatalf("answer not stored: %+v", all[0])
	}
}

func TestPromptOverrideIsOneTimeAndScopedToTheBlockedSecrets(t *testing.T) {
	t.Setenv("BEACON_POLICY_STATE_DIR", t.TempDir())
	now := time.Now()
	if _, ok := LatestPromptBlock("s", now, 10*time.Minute); ok {
		t.Fatal("no block yet")
	}
	_ = Append("s", Entry{At: now, Decision: DecisionBlock, Tool: ToolPrompt, ToolUseID: "prompt:aa", Fingerprints: []string{"aa", "bb"}})
	if _, ok := ConsumePromptOverride("s", []string{"aa"}, now, 10*time.Minute); ok {
		t.Fatal("a block that was not allowed must not pass")
	}
	block, ok := LatestPromptBlock("s", now.Add(time.Minute), 10*time.Minute)
	if !ok || block.ToolUseID != "prompt:aa" {
		t.Fatalf("latest block: %+v %v", block, ok)
	}
	_ = GrantPromptOverride("s", block.ToolUseID, "dev key", now.Add(time.Minute))
	if _, ok := LatestPromptBlock("s", now.Add(time.Minute), 10*time.Minute); ok {
		t.Fatal("an allowed block cannot be allowed again")
	}
	if _, ok := ConsumePromptOverride("s", []string{"aa", "cc"}, now.Add(2*time.Minute), 10*time.Minute); ok {
		t.Fatal("a prompt with another secret must not pass")
	}
	if e, ok := ConsumePromptOverride("s", []string{"bb", "aa"}, now.Add(2*time.Minute), 10*time.Minute); !ok || e.Comment != "dev key" {
		t.Fatalf("the allowed prompt must pass once: %+v %v", e, ok)
	}
	if _, ok := ConsumePromptOverride("s", []string{"aa"}, now.Add(3*time.Minute), 10*time.Minute); ok {
		t.Fatal("an override is used once")
	}

	_ = Append("s", Entry{At: now, Decision: DecisionBlock, Tool: ToolPrompt, ToolUseID: "prompt:dd", Fingerprints: []string{"dd"}})
	if _, ok := LatestPromptBlock("s", now.Add(11*time.Minute), 10*time.Minute); ok {
		t.Fatal("a stale block cannot be allowed")
	}
	_ = GrantPromptOverride("s", "prompt:dd", "late", now)
	if _, ok := ConsumePromptOverride("s", []string{"dd"}, now.Add(11*time.Minute), 10*time.Minute); ok {
		t.Fatal("an expired allowance must not pass")
	}
}
