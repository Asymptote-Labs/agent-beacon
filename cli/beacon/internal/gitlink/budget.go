package gitlink

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

type spawnCounterKey struct{}

// countSpawn adds one to the git spawn counter ctx carries, when it carries one.
func countSpawn(ctx context.Context) {
	if counter, ok := ctx.Value(spawnCounterKey{}).(*atomic.Int64); ok {
		counter.Add(1)
	}
}

// HookRun measures one run of a git hook against its time budget: how long it took, which step it
// was in when its time ran out, and how many git processes it started, which is what decides
// whether the budget fits a slow machine.
//
// A hook calls Start once, Phase as each step begins and Check after each step, then reads Overrun.
// Measuring costs a clock read per step and an atomic add per git process; it spawns nothing and
// touches no file. Only the hook's own goroutine calls its methods; the spawn count is safe to add
// to from any goroutine, so an abandoned upload in pre-push is still counted.
type HookRun struct {
	// Hook is the hook's name: post-commit, post-rewrite or pre-push.
	Hook string
	// Budget is the time the hook allows itself.
	Budget time.Duration
	// Now is the clock; nil means time.Now. Tests pin it.
	Now func() time.Time
	// OnPhase, when set, is called as each phase begins, after the phase's start is read. Tests use
	// it to move the clock and end the hook's time in a chosen phase.
	OnPhase func(phase string)

	spawns      atomic.Int64
	start       time.Time
	phase       string
	phaseStart  time.Time
	ranOut      string
	ranOutAfter time.Duration
	cutShort    bool
}

func (r *HookRun) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Start begins the run and returns ctx carrying its git spawn counter: every git process started
// with the returned context, or one derived from it, is counted.
func (r *HookRun) Start(ctx context.Context) context.Context {
	r.start = r.now()
	return context.WithValue(ctx, spawnCounterKey{}, &r.spawns)
}

// Phase marks the start of the run's next step.
func (r *HookRun) Phase(name string) {
	r.phase, r.phaseStart = name, r.now()
	if r.OnPhase != nil {
		r.OnPhase(name)
	}
}

// Check records that the current phase ran out of time when ctx's deadline has passed, and reports
// whether it had. err is the step's own result: a step that failed once its deadline had passed was
// cut short by it, and one that succeeded finished late. Cancellation that is not a deadline (an
// interrupted git) is not a budget overrun and is not recorded.
func (r *HookRun) Check(ctx context.Context, err error) bool {
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false
	}
	r.RanOut(err != nil)
	return true
}

// RanOut records that the current phase ran out of time; lost says whether that left the hook's
// work undone. The first phase to run out is the one recorded, because the steps after it run on
// what is left; lost accumulates over every call.
func (r *HookRun) RanOut(lost bool) {
	if r.ranOut == "" {
		r.ranOut = r.phase
		r.ranOutAfter = r.now().Sub(r.phaseStart)
	}
	r.cutShort = r.cutShort || lost
}

// Overrun returns what the run recorded, and false when it never ran out of time. Elapsed time and
// the spawn count are read now, so they cover the whole run up to the call.
func (r *HookRun) Overrun() (schema.VCSHookInfo, bool) {
	if r.ranOut == "" {
		return schema.VCSHookInfo{}, false
	}
	return schema.VCSHookInfo{
		Name:           r.Hook,
		Phase:          r.ranOut,
		BudgetMS:       r.Budget.Milliseconds(),
		ElapsedMS:      r.now().Sub(r.start).Milliseconds(),
		PhaseElapsedMS: r.ranOutAfter.Milliseconds(),
		GitSpawns:      r.spawns.Load(),
		CutShort:       r.cutShort,
	}, true
}

// Event returns the git.hook_budget_exceeded event for the run, and false when it never ran out of
// time. repoRoot is the repository's work tree, empty when the hook ran out before resolving it.
// The event is Beacon's own (harness beacon) and carries the hook's name, phase, timings and git
// spawn count only: no path but the repository's, no command and no commit content.
func (r *HookRun) Event(repoRoot, agentVersion string) (schema.Event, bool) {
	hook, ok := r.Overrun()
	if !ok {
		return schema.Event{}, false
	}
	severity := schema.SeverityInfo
	outcome := "it finished late"
	if hook.CutShort {
		severity = schema.SeverityLow
		outcome = "its work was cut short"
	}
	event := schema.NewEvent(schema.NewEventOptions{
		Action:       asymptoteobserve.ActionGitHookBudgetExceeded,
		Category:     "system",
		Severity:     severity,
		AgentVersion: agentVersion,
		Harness:      schema.HarnessInfo{Name: "beacon"},
		Fidelity:     asymptoteobserve.FidelityObserved,
		Message: fmt.Sprintf("%s hook ran out of time in %s: %s of its %s budget, %d git process(es); %s",
			hook.Name, hook.Phase, formatMS(hook.ElapsedMS), formatMS(hook.BudgetMS), hook.GitSpawns, outcome),
	})
	event.Timestamp = schema.FormatTimestamp(r.now())
	event.Event.Kind = "beacon_system"
	event.Repository = repoRoot
	event.VCS = &schema.VCSInfo{Hook: &hook}
	return event, true
}

// formatMS renders milliseconds as seconds for a person: 3200 is 3.2s.
func formatMS(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}

// BudgetOverrun is one git.hook_budget_exceeded event read back from the runtime log.
type BudgetOverrun struct {
	Timestamp  string `json:"timestamp"`
	Repository string `json:"repository,omitempty"`
	schema.VCSHookInfo
}

// BudgetOverruns counts the git hook runs that ran out of time since a point in time.
type BudgetOverruns struct {
	Since    string `json:"since"`
	Count    int    `json:"count"`
	CutShort int    `json:"cut_short"`
	// Last is the most recent overrun, nil exactly when Count is 0.
	Last *BudgetOverrun `json:"last,omitempty"`
}

// ScanBudgetOverruns reads the git.hook_budget_exceeded events recorded at or after since in the
// runtime log at logPath and the rotated archives kept beside it, since a busy log rotates more
// often than the window covers. repoRoot, when set, keeps only the events of that work tree. Only
// lines that mention the action are decoded; a file that cannot be read counts nothing.
func ScanBudgetOverruns(logPath, repoRoot string, since time.Time) BudgetOverruns {
	summary := BudgetOverruns{Since: schema.FormatTimestamp(since)}
	var lastAt time.Time
	for _, path := range writer.RetainedLogPaths(logPath) {
		scanBudgetOverrunFile(path, repoRoot, since, &summary, &lastAt)
	}
	return summary
}

func scanBudgetOverrunFile(path, repoRoot string, since time.Time, summary *BudgetOverruns, lastAt *time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	marker := []byte(`"` + asymptoteobserve.ActionGitHookBudgetExceeded + `"`)
	reader := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, marker) {
			var event schema.Event
			if json.Unmarshal(line, &event) == nil && event.Event.Action == asymptoteobserve.ActionGitHookBudgetExceeded && event.VCS != nil && event.VCS.Hook != nil &&
				(repoRoot == "" || event.Repository == repoRoot) {
				if at, perr := asymptoteobserve.ParseTimestamp(event.Timestamp); perr == nil && !at.Before(since) {
					summary.Count++
					if event.VCS.Hook.CutShort {
						summary.CutShort++
					}
					if summary.Last == nil || !at.Before(*lastAt) {
						*lastAt = at
						summary.Last = &BudgetOverrun{Timestamp: event.Timestamp, Repository: event.Repository, VCSHookInfo: *event.VCS.Hook}
					}
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// Describe is the overrun in a few words for a person: "post-commit in attribute, 3.2s of 3s,
// cut short".
func (o BudgetOverrun) Describe() string {
	outcome := "finished late"
	if o.CutShort {
		outcome = "cut short"
	}
	return fmt.Sprintf("%s in %s, %s of %s, %s", o.Name, o.Phase, formatMS(o.ElapsedMS), formatMS(o.BudgetMS), outcome)
}
