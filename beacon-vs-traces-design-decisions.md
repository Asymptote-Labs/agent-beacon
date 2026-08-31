# Beacon vs Traces — the design decisions and what they cost

Ten forks where Beacon and the Traces CLI made opposite calls, what each call buys, and what it
bills for. Sources: teardown of the Traces v0.6.23 Bun binary, cross-checked against the v0.6.19
local-store inspection in *Beacon Revamp §2*; Beacon figures from the 5,595-event runtime.jsonl
snapshot in that same document.

The purpose is not to score the two products. It is to separate the differences that are
**deliberate and worth holding** from the ones that are **accidental and worth fixing** — because
Beacon currently has both, and they look alike from inside.

---

## At a glance

| # | Decision axis | Beacon chose | Traces chose | Verdict |
|---|---|---|---|---|
| 1 | Trust boundary | Local-only; customer-controlled SIEM | Cloud-first; vendor backend is the product | Different bets |
| 2 | Collection mechanism | Synchronous hooks — the payload *is* the event | Post-hoc transcript scraping; hooks are triggers | **Beacon**, decisively |
| 3 | Data model | Flat event, typed sub-objects | Trace → Message → Part | **Beacon**, for its job |
| 4 | Event taxonomy | Open action namespace | 6 closed types, each with a required payload | **Traces** |
| 5 | Storage substrate | Append-only JSONL | SQLite + FTS5 | Different bets |
| 6 | Identity & ordering | Derived UUIDs + sequence — designed, not wired | UUIDv5 + `order` int, shipped day one | **Traces**, on execution |
| 7 | Raw retention | `raw` kept on every event | No equivalent | **Beacon** |
| 8 | Content controls | Retention modes, redaction, truncation, size limits | None | **Beacon** |
| 9 | Detection & enforcement | 75 CEL rules + policy seam | None | **Beacon** |
| 10 | Primary consumer | Security analyst / SIEM | The coding agent itself | Convergent |

Three of these — 4, 6, and the sharing half of 10 — are places where Traces made the call Beacon
should have made. They are also all cheap to close, because they are discipline rather than
architecture.

---

## 1. Trust boundary

**The fork.** Beacon processes everything locally and forwards to a SIEM the customer already owns;
there is no account, and the only egress is one bounded install-time signup POST. For Traces the
local SQLite is a cache — the product is `api.traces.com`, and device-code OAuth gates the core
loop (share, sync, namespaces, orgs).

**Impact.** This decides who can adopt without procurement. Beacon installs into a regulated
environment with no DPA and no vendor trust; Traces cannot. In exchange Traces has a collaboration
surface, cross-machine continuity, and a monetizable seat on day one, and Beacon has none of those
until a trace object exists to share.

| Approach | Pros | Cons |
|---|---|---|
| **Beacon** — local-only, customer SIEM | No data egress; no DPA; works air-gapped; the security buyer can say yes without legal; forwarding is native rather than an integration | Every install is an island; no team surface, no cross-device continuity, no network effect; harder to demo value in five minutes |
| **Traces** — vendor backend | Instant team value; a shareable URL; cross-machine continuity; a natural place to charge | Blocked in regulated environments; requires vendor trust *and* auth before the core loop works; full transcript egress with no redaction (see §8) |

---

## 2. Collection mechanism

**The fork.** In Beacon the hook payload *is* the telemetry: it runs synchronously, before the tool
executes, and can deny. In Traces the installed hook is `traces hook agent <event> --agent <id>`,
and its stdin handler extracts only a session ID; the real data comes from parsing the runtime's own
session store afterwards.

**Impact.** This is the product-defining difference and it is not close. Nothing in Traces runs
before a tool executes, so it structurally cannot be a control plane — no amount of engineering
gets it there from a scraping architecture. Beacon pays for that with per-runtime integration work
and coverage gated on whatever each runtime's hook API happens to expose.

| Approach | Pros | Cons |
|---|---|---|
| **Beacon** — synchronous hooks | Real-time; can deny; approval decisions *with outcomes*; exit codes, durations, diffs; the only path on which enforcement is possible at all | Per-runtime integration cost; coverage capped by each hook API — Cursor yields 79 events against 78,430 rows in its own store, and only 8% of approvals (41 of 469) are visible at decision time |
| **Traces** — post-hoc scraping | 16 runtimes with zero runtime cooperation; the complete transcript, not the hook's subset; immune to hook API gaps | After-the-fact only; no enforcement, ever; 0 per-tool approval records; permanently fragile against undocumented on-disk formats — its Claude Code adapter alone carries ~15 hardcoded skip lists |

**The honest read.** Their breadth argument is real. Scraping gets `hermes`, `openclaw`,
`antigravity` and `prime-agent` for free. Beacon's answer is not to abandon hooks but to add
scraping as a third *enrichment-only* source — which is exactly WS6, and why its
`event.source: "scrape"` marker never feeding an enforcement decision is the constraint that keeps
this decision intact.

---

## 3. Data model

**The fork.** Beacon: one flat event with typed optional sub-objects (`command`, `file`, `tool`,
`approval`, `mcp`, `content`). Traces: a conversation tree of Trace → Message → Part.

**Impact.** Shape determines what is cheap. A CEL rule reads `command.command` directly and a SIEM
indexes a flat line with no transform — both free in Beacon's shape, both requiring a flattening
pass in Traces'. The inverse is equally true: Traces renders a conversation natively and has an
obvious unit to share, and Beacon has neither, which is precisely the WS7 gap.

| Approach | Pros | Cons |
|---|---|---|
| **Beacon** — flat + typed sub-objects | Directly predicable by CEL; SIEM-native with no adapter; one line = one action; stable field contract | No native rendering of a conversation; no human-facing unit between "event" and "the whole log" |
| **Traces** — Trace/Message/Part | Renders as a conversation with no work; a natural, obviously shareable unit; reads well to a human or an LLM | No predicate surface — every analytic question needs a flattening pass first; forwarding requires an adapter that does not exist |

**Do not converge here.** Adopt Traces' *identity and typing discipline* inside the flat model.
Adopting its shape would fork the schema and cost the two things the flat model is for.

---

## 4. Event taxonomy

**The fork.** Traces has 6 closed event types, each with a required payload. Beacon has an open
action namespace — 20 observed values, no action constants anywhere in the code.

**Impact.** Openness is why Beacon's capture breadth grew as fast as it did, and it is also why
`session.activity` is 49% of the log holding nine distinct meanings separable only by parsing a
message string, and why **41% of events carry no `command`, `file`, `tool`, `prompt`, `mcp` or
`approval` object at all**. Every detection rule predicates on those objects, so 41% of the log is
unmatchable by construction. That is not a capture problem; it is a taxonomy problem.

| Approach | Pros | Cons |
|---|---|---|
| **Beacon** — open namespace | New signal ships without schema work; no upstream coordination; breadth grows fast | 49% of events in one bucket with nine meanings; 41% unmatchable by any rule; rule authors cannot tell a fact from a guess; no compiler help |
| **Traces** — closed set with required payloads | Every event is guaranteed queryable; the renderer can be total; no string-parsing to recover meaning | New signal needs schema work; awkward fit for a runtime that does not map onto the six; 26% of its own events are still hook-execution noise, so closure alone does not buy signal |

**Traces is right here** — and it is WS4. Note the caveat though: their 26% hook-execution records
against our 34% shows a closed taxonomy does not by itself eliminate noise. It makes the noise
*labelled* rather than *unbucketed*, which is the actual win.

---

## 5. Storage substrate

**The fork.** Append-only JSONL as a release contract versus SQLite with an FTS5 index and cursor
tables.

**Impact.** JSONL forwards to any SIEM with zero adapters and is safe to append from several
writers at once; it is bad at random access, which is why anything trace-shaped needs a rebuildable
index over it. SQLite gives instant list/search/show and incremental cursors, and has no forwarding
story whatsoever — nothing tails a SQLite file.

| Approach | Pros | Cons |
|---|---|---|
| **Beacon** — append-only JSONL | Wazuh/Elastic/Sentinel/S3 forwarding with no adapter; multi-writer append-safe; a line is portable and diffable; the format *is* the contract | No random access; list/search/show require a full scan or a separate index; rotation and archives complicate every read path |
| **Traces** — SQLite + FTS | Instant search, list and show; incremental cursors so a 3.4 GB store is not re-read; transactional | Zero forwarding story; not tailable; the store becomes the source of truth, so schema migrations are load-bearing |

**Not either/or.** The right answer is what WS7 proposes: JSONL stays the truth, and a
SQLite-shaped index sits over it as a cache that can be deleted with no change in behavior. Traces'
`indexes(agent_id, cursor_json, last_scan_at)` table is the one piece worth lifting verbatim.

---

## 6. Identity and ordering

**The fork.** Both sides decided identity should be deterministic. Traces shipped it: UUIDv5 plus an
`order` integer per trace, from day one. Beacon designed it more carefully — `EventIDForLine` with
two identity modes, `Event.Sequence`, `ToolCallIDKeys` — and then did not wire it.

**Impact.** This is the single clearest case where the *decision* was right on both sides and only
one side finished. The measured consequences: `gen_ai.tool.call.id` populated on **0 of 5,595**
events while sitting in `raw` on 856; **0** events with sub-second timestamps and 97% sharing a
timestamp with a sibling; correlation falling back to file-append order, **wrong 258 times** by up
to 10 seconds; and dedup structurally inert since August, so 48% of distinct shell commands and
100% of file edits are recorded twice.

| Approach | Pros | Cons |
|---|---|---|
| **Beacon** — derived, two-mode identity | Derivable rather than asserted, so no writer can be wrong; one ID across both capture paths; works on a forwarded log; content-digest fallback means nothing is unidentified | Unpopulated today, so every downstream property it enables is currently hypothetical; the sophistication made it easy to defer |
| **Traces** — stored UUIDv5 + order int | Shipped and working; idempotent upload and append-refresh fall out of it; ordering is never in question | Stored, so it can drift from the events it names; the `order` int is per-trace and cannot reconcile two capture paths — a problem they avoid by only having one |

**The lesson is about sequencing, not design.** Beacon's derivation is better; a design that is not
wired buys nothing. WS1 and WS2 are alias lists and a timestamp format, not new capture.

---

## 7. Raw retention

**The fork.** Beacon keeps a `raw` object on every event. Traces retains no equivalent.

**Impact.** Every number in the revamp document came out of `raw` — including the discovery that
the call ID, the sequence number and the assistant's response text were all already arriving. Beacon
can diagnose and repair a mapping bug retroactively, across data already collected. Traces cannot:
its adapters must be right the first time or the signal is gone permanently, which is a heavy tax on
16 scraped runtimes with undocumented formats.

| Approach | Pros | Cons |
|---|---|---|
| **Beacon** — retain `raw` | Retroactive repair of mapping bugs; new promotions apply to historical data; the log audits itself | Log size; an unbounded surface for whatever the runtime put in it, so redaction has to cover it; and it makes deferring promotion painless — 856 call IDs, 3,667 sequences and 254 responses are sitting there unpromoted |
| **Traces** — no raw | Compact; nothing unreviewed leaves the adapter | An adapter bug is unrecoverable; no way to answer "what were we dropping"; every format change is a silent data loss event |

---

## 8. Content controls

**The fork.** Beacon has retention modes, redaction, truncation and event-size limits. In the Traces
binary there is no redaction, no sanitization and no exclusion mechanism — the only gate is a
directory-level share on/off rule and a three-level visibility flag.

**Impact.** This decides whether the artifact can leave the machine at all in an enterprise. It is
also what makes a safe export default *possible*: a share path can default to `redacted` only if a
redaction engine already exists.

| Approach | Pros | Cons |
|---|---|---|
| **Beacon** — full content controls | Content can leave the machine under a stated policy; a recipient can be told what they are not seeing; enterprise-viable | Real engineering cost; every new field must be routed through it; defaults have to be chosen twice — once for local logging, once for export |
| **Traces** — none | Nothing to configure; the shared trace is complete and therefore maximally useful | Full prompts, tool inputs, command output and thinking go to the vendor verbatim; no path to an enterprise deployment without building this first |

---

## 9. Detection and enforcement

**The fork.** Beacon ships 75 CEL rules with conformance fixtures and an off-by-default policy seam.
Traces has no detection layer of any kind — the `severity` and `detection` strings in its binary are
Sentry log levels and agent-discovery paths.

**Impact.** Beacon is the only one of the two that can answer *"did anything bad happen"*. This is
the moat, and it is downstream of decisions 2 and 4: it needs synchronous capture to act, and a
matchable payload to fire. The 41% payload-free share from §4 is a direct tax on this capability.

| Approach | Pros | Cons |
|---|---|---|
| **Beacon** — rules + policy seam | Answers the question the security buyer is actually asking; open rule format with fixtures; enforcement possible without shipping enforcement in the open build | Only as good as the payload underneath it — 41% of events cannot match any rule; rules must be maintained against a moving runtime surface |
| **Traces** — none | Nothing to maintain; no false positives to defend; the product stays simple | Cannot answer the security question at all; no path to it from a scraping architecture |

---

## 10. Primary consumer, and the sharing unit

**The fork.** Traces treats the coding agent as the primary *user* — `traces search-instructions` is
a command whose entire output is a prompt for an LLM, and it installs a `share-to-traces` skill into
each runtime's skills directory. Beacon treats the agent as the *subject*, and the consumer is an
analyst or a SIEM.

**Impact.** This shapes every surface: Traces optimizes for recall and readability, Beacon for
precision and matchability. The two are less exclusive than they look — Beacon already ships an MCP
server with `search_activity`, `summarize_activity` and `get_activity_event`, which is the same
posture arrived at from the other direction.

The sharing half is where the gap is real. Traces shares **per trace**, with three visibility
levels, a URL, and append-refresh onto an existing remote object. Beacon's current story is a
whole-log gzip to a bucket — not a unit anyone can hand to anyone.

| Approach | Pros | Cons |
|---|---|---|
| **Beacon** — analyst/SIEM consumer, whole-log export | Precision; a stable schema a rule and a SIEM both read; no re-encoding | No human-scale unit to share; "send me what happened" has no answer smaller than the entire log; nothing addressable |
| **Traces** — agent consumer, per-trace sharing | An obvious unit; idempotent append-refresh; a URL; agents can query their own history as memory | Optimized for recall over precision; no forwarding; sharing requires the vendor backend, so there is no offline artifact at all |

**This is the WS7 opportunity**, and Beacon can land in a better place than either: a per-trace
bundle whose events are byte-identical to log lines is simultaneously the human-shareable unit
Traces has *and* replayable through `beacon scan` and forwardable to a SIEM, which Traces' share
format can never be.

---

## Coda — distribution posture

Not a design decision about telemetry, but it shapes who can deploy.

| | Beacon | Traces |
|---|---|---|
| Artifact | GoReleaser archives + checksums; signed, notarized, stapled macOS `.pkg` | Unsigned raw executables installed straight from a GitHub release |
| Updates | Explicit `beacon endpoint update --apply` | `autoupdate: true` — the binary replaces itself |
| Self-telemetry | None | Sentry DSN hardcoded and enabled |
| Size | Go static binary + separate collector | 140 MB Bun bundle (JavaScriptCore, tree-sitter wasm, OpenTUI native `.so`) |
| Repo footprint | None | Writes git notes at `refs/notes/traces` and pushes them to the remote |

Worth saying out loud in positioning: an endpoint agent that is signed, notarized, and does not
update itself without being asked is a materially easier security review than one that does the
opposite.

---

## What to hold, what to fix

**Hold, deliberately.** Synchronous hooks (2). The flat event shape (3). JSONL as the forwarding
contract (5). `raw` retention (7). Content controls (8). Detection and the policy seam (9). Every
one of these is a reason Beacon can do something Traces structurally cannot.

**Fix, because the difference is accidental.** The open taxonomy that put 41% of events beyond any
rule (4 → WS4). Identity and ordering that were designed and never wired (6 → WS1, WS2). The
missing shareable unit (10 → WS7). None of the three is architectural; all three are discipline
Traces applied earlier.

**The uncomfortable one.** Their scraping architecture reaches 16 runtimes with no cooperation from
any of them, and ours reaches Cursor at 79 events against 78,430 rows. Adding scraping as an
enrichment-only third source (WS6) closes that without giving up decision 2 — but only if the
`event.source: "scrape"` provenance marker is enforced from the first commit rather than retrofitted,
because the moment a scraped event can influence an enforcement decision, decision 2 is quietly gone.
