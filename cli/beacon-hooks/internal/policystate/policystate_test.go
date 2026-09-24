package policystate

import (
	"fmt"
	"testing"
	"time"
)

func TestAppendKeepsTheMostRecent(t *testing.T) {
	t.Setenv("BEACON_POLICY_STATE_DIR", t.TempDir())
	for i := 0; i < 12; i++ {
		if err := Append("sess/../1", Entry{At: time.Now(), Decision: "deny", Target: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	got := Load("sess/../1")
	if len(got) != maxEntries || got[0].Target != "2" || got[9].Target != "11" {
		t.Fatalf("got %+v", got)
	}
	if Load("other") != nil {
		t.Fatal("sessions must not share state")
	}
}
