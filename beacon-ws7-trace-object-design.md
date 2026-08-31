# WS7 — Session & Trace Object

Design doc for the workstream defined in *Beacon Revamp §4 / WS7*.

**Goal.** Give Beacon a first-class, addressable *trace*: one bounded, ordered, identity-bearing
object per agent session, computed from the event log rather than stored beside it — and make it
the artifact that trace sharing later publishes, so sharing is a sink over an existing object
rather than a new pipeline.

**Non-goal.** Changing the event schema's shape. The flat event with typed sub-objects stays
exactly as it is. A trace is a *header plus an ordered range*, never a re-encoding of events.

---

## 1. The three decisions this doc makes

Everything else follows from these.

| # | Decision | Rejected alternative |
|---|---|---|
| 1 | **The trace is a projection, not a record.** `trace.Project(events) Trace` is the only definition. Any on-disk form is a cache that can be deleted with no behavior change. | Materializing traces into a second store (SQLite / a `traces.jsonl`). Creates a second source of truth and guarantees drift. |
| 2 | **The trace ID is derived, never asserted.** `TraceIDForSession(harness, sessionID)` → UUIDv5, same discipline as `EventIDForLine`. No new field is written into events. | Stamping a `trace_id` into every event. Requires a schema change, a migration, and a writer that can't be wrong — and events already carry everything needed to derive it. |
| 3 | **The share unit is a bundle whose `events.jsonl` lines are byte-identical to runtime log lines.** Every destination — file, S3/GCS, a hosted endpoint later — is a sink over that one bundle format. | Re-encoding events into a share-specific wire format. That is exactly the fork that makes Traces' adapters have to be right the first time. |

Decision 3 is the one that makes sharing cheap later: a bundle is replayable through `beacon scan`,
forwardable to a SIEM, and diffable against the source log, because it *is* the source log, filtered.

---

## 2. Naming collision — resolve this first

`Event.Trace` already exists and means the **OpenTelemetry** trace:

```go
type TraceInfo struct {
    ID           string `json:"id,omitempty"`
    SpanID       string `json:"span_id,omitempty"`
    ParentSpanID string `json:"parent_span_id,omitempty"`
}
```

A session trace is a different thing at a different altitude. Rules:

- The event field `trace.id` keeps its OTel meaning. Untouched. It is a release contract.
- The new object is a **session trace**, addressed as `trace` only at the CLI/API surface
  (`beacon trace show`, `/api/traces`), where there is no ambiguity.
- Inside Go, the type is `trace.Trace` in a package named for sessions, and any reference to the
  OTel value is spelled `event.Trace.ID` at the call site so the two never read alike.
- Nothing writes a session-trace ID into an event. Per Decision 2 it is derived, so the collision
  never reaches the schema.

---

## 3. Identity

```go
// TraceIDForSession derives the deterministic identity of one agent session.
//
// Derived, not asserted: the same session yields the same ID in any process, any
// release, and on any machine that read the same events -- so a trace exported
// twice, or exported from a forwarded copy of the log, addresses one object
// rather than two.
//
// Inputs are the normalized harness name and the runtime's own session ID, and
// nothing else. Hostname is deliberately excluded so a log forwarded off the
// endpoint still derives the ID the endpoint derived. Harness name is included
// because session IDs are only unique within a runtime.
func TraceIDForSession(harness, sessionID string) string
```

Same UUIDv5 construction and the same fixed namespace family as `EventIDForLine`. Like that
constant, the derivation is a contract: changing it renames every trace Beacon has ever addressed,
and a test pins the value.

A session with no ID gets no trace. Events without `session.id` are unattributable by construction
and belong to the log, not to a trace — surfacing them is `beacon scan`'s job, not this object's.

**Why this matters for sharing.** Idempotent upload and append-refresh both need a stable remote
key. Traces gets this from a UUIDv5 stored in its `traces` row; Beacon gets it from a pure function
over data it already has, with no store to keep in sync.

---

## 4. Boundedness — the actual WS7 problem

Today only 2 of 9 sessions record a `session.ended`, and one holds 3,427 events. "This session"
has no defined extent. The fix is to *derive* the boundary and label how it was derived, rather
than depending on a lifecycle event that most runtimes never send.

Closure is evaluated in priority order:

| State | Condition | `ended_reason` | `fidelity` |
|---|---|---|---|
| `closed` | a `session.ended` event was observed | `observed` | `observed` |
| `closed` | no events for longer than the idle window | `idle` | `inferred` |
| `closed` | the log ends and the session was last seen before the retention/rotation horizon | `log_boundary` | `inferred` |
| `open` | still receiving events | — | — |

This reuses `event.fidelity` from `pkg/asymptoteobserve/provenance.go` at trace altitude and adds
nothing new: a boundary Beacon derived is `inferred`, exactly as the existing rule requires for a
synthesized action. It is also the honest answer — a trace that claims it ended when nothing said
so is the same class of error as a synthesized approval.

The session-state logic that already exists inside `dashboard/events.go` — `sessionStateStats`,
`isLifecycleSessionEvent`, `normalizeSessionState`, `isEmptySession` — moves into the projection and
the dashboard calls it instead of owning it. That consolidation is most of WS7's cleanup value.

**Long sessions are not segmented.** One trace per session ID, always. A 3,427-event session is a
real session, not a bug in the boundary. Navigability and share granularity come from turns (§5),
which subdivide a trace without inventing a second identity for it.

---

## 5. Shape

```go
package trace

// Trace is a projection over the events of one agent session. Every field is
// recomputable from those events; nothing here is authoritative on its own.
type Trace struct {
    ID            string      `json:"id"`             // TraceIDForSession
    SchemaVersion string      `json:"schema_version"`

    Session  SessionRef  `json:"session"`             // id, working_directory
    Harness  HarnessRef  `json:"harness"`             // name, version, collection_methods observed
    Endpoint EndpointRef `json:"endpoint,omitempty"`  // hostname, os -- droppable at share time

    State       TraceState `json:"state"`             // open | closed
    EndedReason string     `json:"ended_reason,omitempty"`
    Fidelity    string     `json:"fidelity"`          // of the boundary, not the events
    StartedAt   string     `json:"started_at"`
    EndedAt     string     `json:"ended_at,omitempty"`

    Title      string   `json:"title,omitempty"`      // derived from the first prompt
    Repository string   `json:"repository,omitempty"`
    Branch     string   `json:"branch,omitempty"`
    Models     []string `json:"models,omitempty"`

    Usage  tokens.Usage `json:"usage"`                // reuse tokens.Usage; do not restate it
    Counts Counts       `json:"counts"`
    Turns  []Turn       `json:"turns,omitempty"`

    Detections []DetectionRef  `json:"detections,omitempty"` // rule id + severity, from threatrules
    Range      EventRange      `json:"range"`
    Provenance Provenance      `json:"provenance"`
}

// Counts are the security-relevant tallies. These, not the message count, are
// what make a Beacon trace worth reading.
type Counts struct {
    Events    int `json:"events"`
    Prompts   int `json:"prompts"`
    Tools     int `json:"tools"`
    Commands  int `json:"commands"`
    Files     int `json:"files"`
    MCP       int `json:"mcp"`
    Approvals int `json:"approvals"`
    Denials   int `json:"denials"`
    Errors    int `json:"errors"`
}

// EventRange bounds the trace without copying it. Sequence key is the
// (timestamp, sequence) pair threatrules.SortEvents already orders on.
type EventRange struct {
    FirstEventID string `json:"first_event_id"`
    LastEventID  string `json:"last_event_id"`
    Count        int    `json:"count"`
}
```

`Turn` is the same shape one level down: index, started/ended, prompt title, `EventRange`, `Usage`,
`Counts`, `Detections`. A turn runs from one `prompt.submitted` to the next (or to closure).

Turns earn their place three ways: the dashboard gets a unit to render, sharing gets granularity
(`--turn 4` beats "share the whole 3,427-event session"), and the real-time control plane gets the
scope most rules actually want — *"this turn already had a command denied"* is a more useful
predicate than the same question asked of an eight-hour session.

**What this has that a Traces row cannot.** `Approvals`, `Denials`, `Detections`, and a `Usage`
that is actually populated. Traces declares five token columns and fills them on 0 of 664 traces,
and records 0 per-tool approvals. A shared Beacon trace says *what the agent did, what was refused,
and what tripped which rule* — that is a security artifact. A shared Traces trace is a transcript.
Keep that distinction load-bearing in the header, because it is the whole reason to share one.

---

## 6. Where the code goes

Follow the `tokens` package precedent exactly — a pure function over `[]schema.Event`, no I/O.

| Package | Responsibility |
|---|---|
| `pkg/asymptoteobserve/trace` | `Project(events []Event, opts Options) Trace`, `TraceIDForSession`, `Split(events) []Trace`. Pure, no I/O. In the shared module so the collector, CLI and SDK can all use it. Conformance fixtures like `threatrules`. |
| `cli/beacon/internal/endpoint/tracestore` | Rebuildable index over `runtime.jsonl` + archives. Checkpoint = (file fingerprint, byte offset). Handles rotation. Deletable. |
| `cli/beacon/internal/endpoint/tracebundle` | Bundle writer/reader, manifest, integrity, redaction application at share time. |
| `cli/beacon/cmd/trace.go` | `beacon trace list \| show \| export \| share`. |
| `dashboard/` | `/api/traces`, `/api/traces/{id}` — read-only, per the standing dashboard rule. |
| `mcpserver/` | `list_traces`, `get_trace` alongside the existing `search_activity`. |

The `tracestore` checkpoint is the one idea worth lifting from the Traces binary verbatim: its
`indexes(agent_id, cursor_json, last_scan_at)` table is why it does not re-read a 3.4 GB store on
every invocation. Same problem here as the log grows and archives accumulate. Difference: theirs is
the store, ours is provably a cache.

---

## 7. Sharing

### The bundle

```
<trace-id>.beacon-trace.tar.zst
  manifest.json     trace header + content policy + per-file sha256
  events.jsonl      the ordered, deduplicated canonical events -- unchanged schema
  detections.json   scan results for this trace (optional)
```

The load-bearing property: **`events.jsonl` lines are byte-identical to runtime log lines**, modulo
redaction applied at share time. Consequences, all free:

- a bundle replays through `beacon scan` with no adapter,
- a bundle forwards to a SIEM with no adapter,
- a bundle diffs against the source log to prove nothing was altered,
- and there is no second schema to keep in sync — the failure mode that forces Traces' adapters to
  be right the first time, since it retains nothing equivalent to our `raw`.

`manifest.json` records the exact content policy applied (retention mode, what was redacted,
truncation limits), so a recipient can tell what they are *not* seeing. Same bundle inputs and same
policy produce the same bytes.

### Redaction defaults invert at the boundary

The runtime log defaults to `full` retention because it is local. **A bundle leaves the machine, so
`beacon trace export` defaults to `redacted`**, and `--retention full` must be passed explicitly.
This reuses the existing redaction, truncation and size controls — it does not add a second engine.

This is the single most important posture decision in WS7, and it is precisely where Traces has
nothing: no redaction, no sanitization, no exclusion controls, full transcript content to the vendor
on share.

### Destinations, in posture order

1. `beacon trace export` → writes a bundle file. **No network.** The default, and the whole
   local-only story.
2. `beacon trace share --to <destination>` → reuses existing S3/GCS destination config. Still
   customer-controlled.
3. A hosted publish endpoint is a *later*, opt-in destination implementing the same interface:

```go
// TraceSink accepts a bundle. Every destination is one of these, so a hosted
// backend later is a new implementation rather than a new pipeline.
type TraceSink interface {
    Put(ctx context.Context, b Bundle) (Ref, error)
    // Append uploads only events after ref.LastEventID, for refresh-in-place.
    Append(ctx context.Context, ref Ref, b Bundle) (Ref, error)
}
```

Define the interface in WS7. Do **not** build a backend in WS7. That keeps the public build's
no-network posture intact while making the later work additive.

### Append-refresh

The manifest's `range.last_event_id` plus deterministic event IDs give idempotent re-upload for
free: publish again, and only events after the recorded last ID are new. This is Traces'
`local_message_count` / `remote_message_count` reconciliation, but resting on derived identity
instead of a stored counter, so it cannot drift.

---

## 8. Sequencing — WS7 does not need to wait for WS6

The revamp doc lists WS7 as blocked on WS6. I'd narrow that.

The **projection** needs ordering (WS2) and identity (WS1). Both primitives are already in the tree:
`Event.Sequence` is defined and documented, `ToolCallIDKeys` exists, `EventIDForLine` exists, and
`threatrules.SortEvents` already sorts on `(timestamp, sequence, position)`. What WS6's merge
changes is the *counts inside a trace*, not its shape.

So: **build the projection now, and define it over deduplicated input as a contract rather than
doing its own dedup.** While dedup is inert, a trace's counts are inflated and its structure is
right; when WS1 re-arms dedup on real call IDs, the counts correct themselves with no change to
WS7. Encode that as a fixture pair — one log pre-dedup, one post — asserting the header differs
only in `counts`.

Revised: **WS7 needs WS1 and WS2. It reads better after WS6, but is not blocked by it.**

---

## 9. Making "never a second source of truth" enforceable

Two CI-enforced properties, not doc promises:

1. **Round-trip.** Project a fixture log → export a bundle → re-project from the bundle's
   `events.jsonl` → assert the header is byte-identical. Recomputability becomes a test.
2. **Cache-optional.** Run every trace command with and without `tracestore`, diff the output,
   require equality. Deleting the cache must be undetectable.

Plus the usual house rules: `t.TempDir()`, no root, no network, and conformance fixtures alongside
the projection in the shared module the way `threatrules` does it.

---

## 10. Open questions

These need a call before implementation, not during.

1. **Idle window for inferred closure.** Proposal: 30 minutes, configurable. A trace can reopen if
   events arrive after it — closure is a projection, so this is free and needs no compaction.
2. **Endpoint identity in a shared bundle.** Proposal: drop `hostname` by default at export; keep
   `os`. Hostname is an internal-topology leak in a bundle meant to be sent to someone.
3. **`beacon trace share` on a hosted endpoint** — out of scope for WS7 per §7, but the product
   call on whether it exists at all should be made now, because it decides whether `TraceSink`
   needs auth in its signature.
4. **Turn boundary on runtimes with no prompt event.** Some runtimes (poll-path fx, browser) may
   not expose a clean `prompt.submitted`. Proposal: fall back to one turn per trace and mark
   `turns[].fidelity: inferred`, rather than guessing at boundaries.

---

## Appendix — what was and was not taken from the Traces CLI

Reverse-engineered from the v0.6.23 Bun single-file binary (`market-dot-dev/tap/traces`).

**Taken.** Deterministic UUIDv5 trace identity enabling idempotent upload and append-refresh; a
bounded, closed trace header with denormalized fields; explicit visibility levels; a scan checkpoint
so large stores are not re-read; per-directory share rules as the model for `--to` routing.

**Deliberately not taken.** SQLite as the store of record — JSONL is a release contract. The
Trace → Message → Part conversation model — our flat event with typed sub-objects is the right shape
for CEL predicates and SIEM forwarding, and re-encoding would fork the schema. Auto-share on a
`Stop` hook — silently uploading a session violates the local-only posture. Shipping with no
redaction. Publishing without a local-file destination as the default.
