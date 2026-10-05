package writer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func budgetTestEvent(i int) schema.Event {
	return schema.NewEvent(schema.NewEventOptions{
		Action:   "prompt.submitted",
		Category: "agent",
		Severity: schema.SeverityInfo,
		Harness:  schema.HarnessInfo{Name: "codex"},
		Message:  strings.Repeat("x", 100) + string(rune('a'+i)),
	})
}

func TestBudgetRefusesTheAppendThatWouldNotFit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	budget := NewBudget(1)
	_, err := AppendEvent(budgetTestEvent(0), Options{Path: path, Budget: budget})
	if !errors.Is(err, ErrBudgetSpent) {
		t.Fatalf("AppendEvent over budget = %v, want ErrBudgetSpent", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("a refused append wrote the log: %v", statErr)
	}
	if !budget.Refused() || budget.Used() != 0 {
		t.Fatalf("budget refused=%t used=%d, want refused and nothing used", budget.Refused(), budget.Used())
	}
}

func TestBudgetCountsWrittenBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	budget := NewBudget(1 << 20)
	for i := 0; i < 3; i++ {
		if _, err := AppendEvent(budgetTestEvent(i), Options{Path: path, Budget: budget}); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if budget.Used() != info.Size() {
		t.Fatalf("budget used %d bytes, log holds %d", budget.Used(), info.Size())
	}
	if budget.Refused() {
		t.Fatal("budget reports a refusal it never made")
	}
}

func TestSubBudgetIsBoundByItsParent(t *testing.T) {
	parent := NewBudget(150)
	first := parent.Sub(100)
	if !first.reserve(100) {
		t.Fatal("first share refused bytes within its limit")
	}
	second := parent.Sub(100)
	if second.reserve(60) {
		t.Fatal("second share took bytes its parent no longer had")
	}
	if !second.reserve(50) {
		t.Fatal("second share refused bytes its parent still had")
	}
	if parent.Used() != 150 || parent.Remaining() != 0 {
		t.Fatalf("parent used=%d remaining=%d, want 150 and 0", parent.Used(), parent.Remaining())
	}
	if !second.Refused() || first.Refused() {
		t.Fatalf("refused flags first=%t second=%t", first.Refused(), second.Refused())
	}
}
