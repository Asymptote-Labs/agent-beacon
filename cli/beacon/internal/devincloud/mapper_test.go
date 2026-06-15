package devincloud

import "testing"

func TestMapSessionProducesLifecycleAndMessages(t *testing.T) {
	s := Session{
		SessionID:    "sess1",
		Status:       "suspended",
		UserID:       "user-1",
		CreatedAt:    1000,
		UpdatedAt:    1600,
		AcusConsumed: 2.5,
		PullRequests: []PullRequest{{URL: "https://github.com/acme/widgets/pull/7", Number: 7}},
	}
	msgs := []Message{
		{EventID: "e1", Source: "user", Message: "do the thing", CreatedAt: 1000},
		{EventID: "e2", Source: "devin", Message: "done", CreatedAt: 1100},
	}

	mapped := MapSession(s, msgs)
	if len(mapped) != 3 {
		t.Fatalf("got %d events, want 3 (started, prompt, agent); ended is emitted by the orchestrator", len(mapped))
	}

	wantActions := []string{"session.started", "prompt.submitted", "agent.message"}
	for i, want := range wantActions {
		if mapped[i].Event.Event.Action != want {
			t.Fatalf("event %d action = %q, want %q", i, mapped[i].Event.Event.Action, want)
		}
	}

	// Dedup ids: lifecycle synthetic for started, event_id for messages.
	wantIDs := []string{"sess1:started", "e1", "e2"}
	for i, want := range wantIDs {
		if mapped[i].DedupID != want {
			t.Fatalf("event %d dedup id = %q, want %q", i, mapped[i].DedupID, want)
		}
	}

	// Common fields.
	for _, me := range mapped {
		ev := me.Event
		if ev.Origin != "cloud" {
			t.Fatalf("origin = %q, want cloud", ev.Origin)
		}
		if ev.Harness.Name != "devin" {
			t.Fatalf("harness = %q, want devin", ev.Harness.Name)
		}
		if ev.Run == nil || ev.Run.Provider != "devin_cloud" || ev.Run.RunID != "sess1" || ev.Run.Actor != "user-1" {
			t.Fatalf("run = %+v", ev.Run)
		}
		if ev.Run.Repository != "acme/widgets" {
			t.Fatalf("repository = %q, want acme/widgets", ev.Run.Repository)
		}
		if err := ev.Validate(); err != nil {
			t.Fatalf("event %q failed Validate: %v", ev.Event.Action, err)
		}
	}

	// Prompt text + devin message content.
	if mapped[1].Event.Prompt == nil || mapped[1].Event.Prompt.Text != "do the thing" {
		t.Fatalf("prompt text not mapped: %+v", mapped[1].Event.Prompt)
	}
	if mapped[2].Event.Message != "done" {
		t.Fatalf("agent message = %q, want done", mapped[2].Event.Message)
	}

	// Timestamps come from the source unix times.
	if mapped[0].Event.Timestamp != "1970-01-01T00:16:40Z" {
		t.Fatalf("started ts = %q, want session created_at", mapped[0].Event.Timestamp)
	}
}

func TestEndedEventCarriesMetadata(t *testing.T) {
	s := Session{
		SessionID: "sess1", Status: "suspended", UserID: "user-1",
		CreatedAt: 1000, UpdatedAt: 1600, AcusConsumed: 2.5,
	}
	ended := EndedEvent(s)
	if ended.Event.Action != "session.ended" {
		t.Fatalf("action = %q, want session.ended", ended.Event.Action)
	}
	if err := ended.Validate(); err != nil {
		t.Fatalf("ended failed Validate: %v", err)
	}
	devin, _ := ended.Raw["devin"].(map[string]interface{})
	if devin == nil || devin["acus_consumed"] != 2.5 {
		t.Fatalf("ended raw.devin = %+v", ended.Raw)
	}
	if devin["duration_seconds"] != int64(600) {
		t.Fatalf("duration = %v, want 600", devin["duration_seconds"])
	}
}

func TestMapSessionHasNoEndedRegardlessOfStatus(t *testing.T) {
	for _, st := range []string{"working", "suspended", "finished"} {
		mapped := MapSession(Session{SessionID: "s", Status: st, CreatedAt: 10}, nil)
		if len(mapped) != 1 || mapped[0].Event.Event.Action != "session.started" {
			t.Fatalf("status %q: MapSession should emit only session.started, got %d events", st, len(mapped))
		}
	}
}

func TestStatusClassification(t *testing.T) {
	for _, st := range []string{"finished", "expired", "suspended"} {
		if !IsTerminal(st) {
			t.Fatalf("IsTerminal(%q) = false, want true", st)
		}
	}
	for _, st := range []string{"working", "blocked"} {
		if IsTerminal(st) {
			t.Fatalf("IsTerminal(%q) = true, want false", st)
		}
	}
	if IsFinal("suspended") {
		t.Fatal("suspended should not be final (may resume)")
	}
	if !IsFinal("finished") {
		t.Fatal("finished should be final")
	}
}

func TestObjectNameLayout(t *testing.T) {
	got := ObjectName("agent-traces/team=x", "devin_cloud", "user-1", "sess-9")
	want := "agent-traces/team=x/provider=devin_cloud/user_id=user-1/run_id=sess-9/runtime.jsonl"
	if got != want {
		t.Fatalf("ObjectName = %q, want %q", got, want)
	}
	got2 := ObjectName("", "devin_cloud", "", "")
	want2 := "provider=devin_cloud/user_id=unknown/run_id=unknown/runtime.jsonl"
	if got2 != want2 {
		t.Fatalf("ObjectName empties = %q, want %q", got2, want2)
	}
}
