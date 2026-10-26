// Package harness2session reads the trajectories Harness² (google-research/harness2) records under
// its runs directory and turns them into Beacon endpoint telemetry.
//
// Harness² is not an agent. It is a framework for refining an agent's harness: for each benchmark
// task it runs an executor -- OpenCode or Codex -- several times over the same task with candidate
// harnesses, compares what those executions did, and composes a better harness from the contrast.
// Every one of those executions is a full agent session, and Harness² runs them in places Beacon's
// own integrations for those agents do not see: Codex with its own CODEX_HOME and --ephemeral, so
// no rollout file reaches ~/.codex/sessions, and OpenCode from a source checkout with its event
// stream captured to a file. What Harness² keeps of each execution is that stream, written beside
// the run's other artifacts:
//
//	<runs>/jb/dataset/<profession>/<task>/model_traj/<label>/<task>_<label>_attempt<N>_<stamp>.jsonl
//	<runs>/lab/results/<oc|cx>/<run-id>/opencode_session.jsonl | codex_session.jsonl
//	<runs>/wb/<subset>/.../<trial>/agent/oc-output.txt | codex-output.jsonl
//
// So Beacon reads those files. Two stream formats occur, one per executor: OpenCode's
// `run --format json` events and Codex's `exec --json` thread events. A file is recognized by what
// its first event says it is rather than by its name, because the names differ per benchmark and a
// runs directory holds plenty of other JSONL -- graded results, judge output -- that is not a
// trajectory.
//
// Every event is marked harness.collection_method=poll, as every session-store collector's is:
// Beacon sees an execution once Harness² recorded it, and nothing here can hold or deny a tool call.
package harness2session

import "encoding/json"

// Harness is the canonical harness name every event from this collector carries.
const Harness = "harness2"

// Format names the stream format a trajectory file is written in.
type Format string

const (
	// FormatOpenCode is OpenCode's `opencode run --format json` output: one event per line, each
	// carrying the session id, a millisecond timestamp and the message part it reports.
	FormatOpenCode Format = "opencode_run"
	// FormatCodexExec is Codex's `codex exec --json` output: thread events wrapping thread items.
	// It carries no timestamps.
	FormatCodexExec Format = "codex_exec"
)

// Executor is the agent a format comes from, as Harness² itself names the two (its `--substrate oc`
// and `--substrate cx`) spelled out.
func (f Format) Executor() string {
	switch f {
	case FormatOpenCode:
		return "opencode"
	case FormatCodexExec:
		return "codex"
	default:
		return ""
	}
}

// RunContext is what a trajectory's place in the runs directory says about the execution: which
// benchmark it was for, which task, and which run of it. Every field is optional, because the
// layout differs per benchmark and a hand-run executor's stream need follow none of them.
type RunContext struct {
	// Benchmark is Harness²'s own short name for the benchmark (jb, lab, wb), the first directory
	// under the runs root.
	Benchmark string `json:"benchmark,omitempty"`
	// Subset is the WorkBuddy-Bench subset (office, web, code).
	Subset string `json:"subset,omitempty"`
	// TaskID is the benchmark's task id where the layout names it (JobBench's <profession>/<task>).
	TaskID string `json:"task_id,omitempty"`
	// Label is the harness label the rollout ran under (JobBench names its trajectory directory by it).
	Label string `json:"label,omitempty"`
	// RunID is the run id from the LAB results layout or from the run's completion marker.
	RunID string `json:"run_id,omitempty"`
	// Trial is the WorkBuddy-Bench trial directory the agent stream sits in.
	Trial string `json:"trial,omitempty"`
	// Attempt is the retry number JobBench puts in the file name.
	Attempt int `json:"attempt,omitempty"`
	// HarnessFingerprint is the digest Harness² records in the run's completion marker of the
	// harness tree that produced the rollout, or "base" for the inherited harness. It is what tells
	// two rollouts of the same task apart by the harness they ran with.
	HarnessFingerprint string `json:"harness_fingerprint,omitempty"`
}

// TrajectoryRef is one trajectory file found under a runs root.
type TrajectoryRef struct {
	// Path is the file's absolute path, and the key its cursor is stored under.
	Path string
	// Root is the runs root the file was found under; Rel is its slash-separated path inside it.
	Root string
	Rel  string
	// Format is the stream format the file's first event identified.
	Format        Format
	SizeBytes     int64
	ModTimeUnixMS int64
	Context       RunContext
}

// Line is one complete line of a trajectory, decoded as a JSON object.
type Line struct {
	// Number is the physical line number, counting blank and unparseable lines, so a half-written
	// line at the tail of a live stream does not shift the numbers of the lines before it.
	Number int
	Object map[string]interface{}
}

// Trajectory is a decoded trajectory file.
type Trajectory struct {
	Ref   TrajectoryRef
	Lines []Line
	// LineCount is the number of complete lines the file held when it was read, which is what a
	// cursor is compared against to notice a file rewritten shorter.
	LineCount int
}

// Stats counts what a read could not use.
type Stats struct {
	// Malformed is how many complete, non-blank lines did not decode as a JSON object. OpenCode and
	// Codex print other output to the same stream (a migration banner, a log line), so a few are
	// expected.
	Malformed int
	// PartialTail is set when the file ended in a line with no newline: an event the executor is
	// still writing. It is not read until it is complete.
	PartialTail bool
}

func decodeObject(line []byte) (map[string]interface{}, bool) {
	var object map[string]interface{}
	if err := json.Unmarshal(line, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}
