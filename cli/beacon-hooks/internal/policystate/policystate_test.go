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
