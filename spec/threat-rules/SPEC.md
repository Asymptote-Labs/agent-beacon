# Threat Rules — Specification (v1.2)

An open detection-rule format for AI-agent security threats, native to the Beacon
endpoint event schema. A rule is a YAML document whose match conditions are
[CEL](https://cel.dev) expressions evaluated against a Beacon endpoint event. Rules
carry their own conformance fixtures, so every published rule is self-verifying.

This is the "Sigma for agent telemetry" layer: the rule format and corpus are open; an
evaluation engine that consumes them may be separate (and closed). Any engine that
produces the same `verdict` for a rule's fixtures conforms to this spec.

- Spec version: `threat-rules/v1.2` (see `VERSION` and [Versioning](#versioning)).
- Canonical event schema: the Beacon endpoint event
  (`pkg/asymptoteobserve` `Event`, `schema_version: "1.0"`).
- Machine-readable structural schema: `schema.json` (JSON Schema, draft 2020-12).
- Event fields a rule can match on: `FIELDS.md` (generated from the event schema;
  regenerate with `beacon rules fields --markdown > spec/threat-rules/FIELDS.md`).
- Reference engine and CLI: `pkg/asymptoteobserve/threatrules` is one conformant
  implementation; `beacon scan` runs rules over local telemetry and `beacon rules`
  manages/authors them. See `docs/threat-rules.md`.

## Rule document

```yaml
id: secret-read-then-egress
spec: threat-rules/v1.1   # needed only because this rule uses correlation.order
version: 2
title: Secret-file read followed by network egress
description: >
  Flags a session that reads a credential file and then issues an outbound
  network command within a short window.
severity: high
status: stable
posture: detect
taxonomy:
  owasp_llm: LLM06
  mitre_atlas: AML.T0024

correlation:
  scope: session
  window: 120s
  order: any
  steps:
    - id: read_secret
      match: >
        e.event.action == "file.read" &&
        e.file.path.matches("(\\.env|credentials|id_rsa|\\.aws/)")
    - id: egress
      match: >
        e.event.action == "command.executed" && (
          e.command.command.matches("curl\\s+.*https?://") ||
          e.command.command.matches("\\bwget\\s") ||
          e.command.command.matches("\\bnc\\s+-")
        )

emit:
  reason: "Secret file read then network egress within one session"

tests:
  - name: positive_basic
    verdict: match
    events:
      - { event: { action: file.read }, file: { path: ".env" }, session: { id: s1 } }
      - { event: { action: command.executed }, command: { command: "curl https://x" }, session: { id: s1 } }
  - name: unrelated
    verdict: no_match
    events:
      - { event: { action: file.read }, file: { path: "README.md" }, session: { id: s1 } }
```

A single-event rule omits `correlation` and carries a top-level `match:` (one CEL
expression).

## Fields

Exactly one of `match` or `correlation` must be present. Required:
`id, version, title, severity, status, posture, emit, tests`.

| Field | Required | Type / values | Meaning |
|---|---|---|---|
| `id` | yes | string `^[a-z0-9][a-z0-9-]*$` | Stable unique identity. Unique across the pack. Flows to `policy.id` on a finding. |
| `spec` | no | `threat-rules/v<major>[.<minor>]` (default `threat-rules/v1`) | Minimum spec version the rule needs. Required when the rule uses a feature added after v1: v1.1 or later for `correlation.order`, v1.2 or later for `e.command.parsed_commands`. See [Versioning](#versioning). |
| `version` | yes | int ≥ 1 | Rule content revision; bump on logic change. |
| `title` | yes | string | Human-readable name. Flows to `policy.name`. |
| `description` | no | string | Documentation only; never evaluated. |
| `severity` | yes | `info` \| `low` \| `medium` \| `high` \| `critical` | Mirrors the event severity enum. |
| `status` | yes | `experimental` \| `stable` \| `deprecated` | Maturity; drives conformance gates. |
| `posture` | yes | `detect` \| `enforce-capable` | Observe-only vs. enforcement-eligible. Flows to `policy.enforcement`. |
| `taxonomy` | no | map<string,string> | External references (OWASP/MITRE/CVE), plus `beacon_category`: the corpus directory the rule belongs to (`credential-access`, `prompt-injection`, ...), which survives installation into the flat rule store. No runtime lookup. |
| `match` | one-of | CEL string → bool | Single-event condition over `e`. |
| `correlation` | one-of | object | Multi-event session window (below). |
| `emit.reason` | yes | non-empty string | Finding explanation; flows to `policy.reason`. |
| `tests` | yes | list ≥ 1 | Embedded conformance fixtures (below). |

### `correlation`

| Field | Required | Type / values | Meaning |
|---|---|---|---|
| `scope` | yes | `session` | Grouping key (`e.session.id`). v1 supports `session` only. |
| `window` | yes | Go duration (`120s`, `5m`) | Max elapsed time from the earliest matched event to the latest. |
| `order` | no | `sequence` (default) \| `any` | Whether `steps` must occur in listed order. Since v1.1; a rule that sets it MUST declare `spec: threat-rules/v1.1` or later. |
| `steps` | yes | list ≥ 2 | Step conditions; see `order`. |
| `steps[].id` | yes | string | Step label (diagnostics). Not evaluated. |
| `steps[].match` | yes | CEL string → bool | Per-event condition, same contract as top-level `match`. |

`order` selects how the steps align with events:

- `sequence` (the default when `order` is omitted): the steps form an ordered sequence. Each
  step's event must be at-or-after the previous matched event, and the final step's event
  must be within `window` of the first.
- `any`: each step must be satisfied by a distinct event, in whatever order those events
  occurred, with every matched event within `window` of the earliest one. Use it when the
  risk is the combination rather than the sequence — a secret read and an egress command
  in one session are an exfiltration signal whichever came first, for instance when an
  injected instruction says "run this, then read `.env`".

In both modes a rule produces at most one finding per session, so a session that contains
the steps in both orders still fires once. A single event never satisfies two steps.

### `tests[]`

| Field | Required | Type / values | Meaning |
|---|---|---|---|
| `name` | yes | string | Fixture name. |
| `verdict` | yes | `match` \| `no_match` | Expected outcome when `events` run through an engine. |
| `events` | yes | list of partial events | Ordered input. Each entry follows the Beacon event JSON shape (`event.action`, `command.command`, `file.path`, …). Unspecified fields default to empty. |

## CEL contract

- The event is bound as the variable `e`. Field paths mirror the event JSON
  (`e.event.action`, `e.command.command`, `e.file.path`, `e.prompt.text`,
  `e.gen_ai.usage.input_tokens`, …).
- An expression must type to `bool`.
- The event is presented as a map, so absent sub-objects are null-safe: reference a
  field on a possibly-missing object only behind the value it would carry; engines treat
  a missing path as an empty/zero value (a `match` referencing a missing field yields
  `false`, never an error at evaluation time).
- Regular expressions use RE2 via CEL `.matches(re)` (the same engine as Go `regexp`).
  RE2 runs in time linear in its input and has no backtracking, so a pattern cannot be
  made to run super-linearly (no ReDoS); cost is bounded by the size of the text matched.
- Evaluation is bounded. Macros (`.all`, `.exists`, `.map`, `.filter`) can nest, so an
  expression can do work exponential in its own length. An engine SHOULD cap the work one
  evaluation of one expression against one event may do, and MUST report an evaluation it
  stops as an error for that rule, never as a `match` or `no_match`. The reference engine
  uses cel-go's runtime cost limit at 5,000,000 (`threatrules.MaxEvalCost`); every rule in
  the open corpus spends under 50,000 (a hundredth of the limit) on its fixtures, and under
  1,000,000 (a fifth) on a pessimistic event whose every string field is 64 KiB. A rule
  should stay orders of magnitude below the limit on any event the writer can produce (at
  most 64 KiB).
- Derived fields. `e.gen_ai.tool.call.result_text` is not on the wire: an engine MUST
  compute it from the event before evaluation, and MUST ignore any value an input
  claims for it. It is `gen_ai.tool.call.result` as text (a string as is; for an object
  or list, every string it contains, maps in sorted key order, joined by `\n`, trimmed),
  capped at 4096 bytes and passed through the event writer's secret redaction. One case
  differs: a list element whose `type` is `text`, `image`, `audio`, `document`,
  `resource` or `resource_link` is a content block, and contributes none of its `type`,
  `mimeType`, `annotations`, `_meta` or `sha256` fields, no string `data` or `blob`, and
  no `source` whose `type` is `base64`; the embedded object of a `resource` block is read
  the same way. Objects outside a list are never treated as content blocks. It is
  the empty string unless the event is a read-type tool result — `event.action` is
  `file.read` or `mcp.tool_invoked`, `mcp.server`/`mcp.tool` is set, or the tool is named
  as an MCP tool (`mcp__…`, `MCP:…`) or a web fetch/search tool, or `tool.path` is an
  `http` or `https` URL with a host — and its `content`
  marker, if present, is `included` with a `retention` other than `metadata`. The
  reference derivation is `threatrules.ToolResultText`.
- `e.gen_ai.system_instructions_text` is derived under the same rules (computed, never
  read from input, secret redaction, empty unless the `content` marker, if present, is
  `included` with a `retention` other than `metadata`). It is `gen_ai.system_instructions`
  read in its OpenTelemetry GenAI semconv shape, a list of message parts: the text of each
  part whose `type` is `text` (its `content`, or its `text` when `content` is empty), each
  trimmed and capped at 4096 bytes, in order, joined by `\n`, and the whole capped at 65536
  bytes. A bare string, as the whole value or as a list element, is taken as text; parts of
  any other type contribute nothing. The cap is per part because a skill index holds one
  part per skill, and a cap on the joined text would hide every skill after the first few.
  It is runtime-provided model instructions (for example the skill index on
  `session.context`), never operator-submitted prompt text. The reference derivation is
  `threatrules.SystemInstructionsText`.
- `e.command.parsed_commands` (since v1.2; a rule that selects it MUST declare
  `spec: threat-rules/v1.2` or later) is derived under the same rules (computed, never read
  from input). It is `command.command`, its first 4096 bytes, read as a bash command line: a
  list with one entry per simple command, and per compound command that carries
  redirections, in walk order (each statement, then the statements inside it). Each entry
  has `name` (the base name of the first word after the wrappers `command`, `builtin`,
  `exec`, `nohup`, `time`, and `sudo` and `env` with their options and `NAME=value` words,
  each recognized by its base name so `/usr/bin/env` is `env`; `""` when that word is
  unknown or a wrapper option moves or replaces the command, as `sudo -i` and `env -C` do),
  `argv` (every word after brace expansion, wrappers included,
  as the shell passes it on, or `""` when its value depends on a parameter, command or
  arithmetic substitution, an ANSI-C or locale string, an unquoted glob character, or a
  leading tilde), `argv_raw` (the same words as written), `env` (the `NAME=value`
  assignments before the command, `""` for an unknown value), `file_effects` (the files the
  command evidently writes, `{path, op}`), `pipeline` (shared by the stages of one
  pipeline), `stage` (0-based position in it; a command inside a compound stage such as
  `{ a; b; } | c` or `(a) | c` carries that stage unless it is a stage of an inner pipeline
  or inside a substitution) and `nested` (inside a command or process
  substitution, or inside a `-c` script). When `sh`, `bash`, `zsh`, `dash` or `ksh` appears
  as a word of a command with an option cluster containing `c`, its first operand, if known,
  is read the same way, at most two levels deep, and its commands follow the shell's with
  `nested` true and pipeline numbers of their own. Here-document bodies and the strings of
  other commands are not read as commands. A command line that ends early (an unclosed
  quote, here-document, substitution or compound command) yields the commands before the
  point where it became incomplete; one that does not parse yields the empty list, so a
  rule that must not miss such captures keeps a `command.command` fallback for it. The
  reference derivation is `threatrules.ParsedCommands` over `shellcmd.Parse`, and the file
  effects are `shellcmd.FileEffects`.
- `e.system_context` is on the wire, not derived: on `session.context` it names what the
  runtime gave the model. `kind` is `skill_listing` for a skill index, `source` is where
  Beacon read it (`system_prompt`, `transcript`), and `skills` names each listed skill as
  `skill_name` and `skill_name_hash`, so a rule can select an index without matching its
  text: `e.system_context.skills.exists(s, s.skill_name == "deploy")`.
- An engine MUST reject (at load) any rule whose CEL expression fails to compile or does
  not type to `bool`. Because field paths are checked against the event schema, a typo
  like `e.fil.path` is a load-time error.

## Verdict model

An engine evaluates the sequence it is given; putting a real event stream into the order the
events happened is the caller's responsibility. Beacon events carry a nanosecond `timestamp`
and an optional per-writer `sequence` for that purpose, and `beacon scan` sorts by
`(timestamp, sequence)` before evaluating — the order lines appear in a log is not their
emission order.

An engine produces one of two verdicts for a rule against an ordered event sequence:

- `match` — the rule fired.
- `no_match` — it did not.

Conformance is defined entirely in these terms: for each fixture, an engine MUST produce
the fixture's declared `verdict`.

## Versioning

A spec version is `threat-rules/v<major>.<minor>`; `v1` is `v1.0`. A minor version only adds
optional fields, so every rule valid under an earlier minor stays valid. A major version may
change meaning.

A rule declares the version it needs in `spec`, not the version current when it was written,
so a corpus mixes versions and a rule moves to a newer `spec` only when it adopts a newer
feature. That keeps the rest of a corpus loadable by an older engine.

An engine MUST read a rule's `spec` before it rejects any unknown field, and MUST NOT evaluate
a rule whose `spec` is newer than the version it implements. It SHOULD set that rule aside with
a diagnostic naming the rule, the version the rule needs, and the version the engine supports,
and continue loading the rest of the corpus. The reference engine skips such rules with a
warning in `beacon scan`, `beacon rules list`, `add`, and `pull`, and reports them as `SKIP`
lines that fail `beacon rules lint`.

Engines released before v1.1 do not read `spec`: they reject a rule that carries it, or any
v1.1 field, as an unknown field, and refuse the directory that contains it.

### Changelog

- **v1.2**: the derived field `e.command.parsed_commands`.
- **v1.1**: `correlation.order` (`sequence` default, or `any` for order-insensitive steps),
  and the per-rule `spec` field with the engine rules above.
- **v1**: initial version.

## Maturity ladder

`status` gates what a rule must prove:

- `experimental` — valid (all CEL compiles) and ≥ 1 fixture.
- `stable` — valid, **and** at least one `match` fixture **and** at least one `no_match`
  fixture, **and** all fixtures produce their declared verdict.
- `deprecated` — must still parse and validate; fixtures optional.

A pack MUST NOT contain duplicate `id`s.

## Conformance

An engine conforms if, for every rule in a pack:

1. It loads and validates the rule (structural + CEL compile/type-check).
2. It enforces the maturity gate for the rule's `status`.
3. For every fixture, evaluating `events` yields the declared `verdict`.

The reference implementation lives in
`pkg/asymptoteobserve/threatrules` and is exercised by its conformance test.
