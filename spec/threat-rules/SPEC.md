# Threat Rules — Specification (v1)

An open detection-rule format for AI-agent security threats, native to the Beacon
endpoint event schema. A rule is a YAML document whose match conditions are
[CEL](https://cel.dev) expressions evaluated against a Beacon endpoint event. Rules
carry their own conformance fixtures, so every published rule is self-verifying.

This is the "Sigma for agent telemetry" layer: the rule format and corpus are open; an
evaluation engine that consumes them may be separate (and closed). Any engine that
produces the same `verdict` for a rule's fixtures conforms to this spec.

- Spec version: `threat-rules/v1` (see `VERSION`).
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
version: 1
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
| `version` | yes | int ≥ 1 | Rule content revision; bump on logic change. |
| `title` | yes | string | Human-readable name. Flows to `policy.name`. |
| `description` | no | string | Documentation only; never evaluated. |
| `severity` | yes | `info` \| `low` \| `medium` \| `high` \| `critical` | Mirrors the event severity enum. |
| `status` | yes | `experimental` \| `stable` \| `deprecated` | Maturity; drives conformance gates. |
| `posture` | yes | `detect` \| `enforce-capable` | Observe-only vs. enforcement-eligible. Flows to `policy.enforcement`. |
| `taxonomy` | no | map<string,string> | External references (OWASP/MITRE/CVE). No runtime lookup. |
| `match` | one-of | CEL string → bool | Single-event condition over `e`. |
| `correlation` | one-of | object | Multi-event ordered window (below). |
| `emit.reason` | yes | non-empty string | Finding explanation; flows to `policy.reason`. |
| `tests` | yes | list ≥ 1 | Embedded conformance fixtures (below). |

### `correlation`

| Field | Required | Type / values | Meaning |
|---|---|---|---|
| `scope` | yes | `session` | Grouping key (`e.session.id`). v1 supports `session` only. |
| `window` | yes | Go duration (`120s`, `5m`) | Max elapsed time from first matched step to final matched step. |
| `steps` | yes | list ≥ 2, ordered | Ordered sequence; each step's event must be at-or-after the previous matched event and within `window` of the first. |
| `steps[].id` | yes | string | Step label (diagnostics). Not evaluated. |
| `steps[].match` | yes | CEL string → bool | Per-event condition, same contract as top-level `match`. |

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
- An engine MUST reject (at load) any rule whose CEL expression fails to compile or does
  not type to `bool`. Because field paths are checked against the event schema, a typo
  like `e.fil.path` is a load-time error.

## Verdict model

An engine produces one of two verdicts for a rule against an ordered event sequence:

- `match` — the rule fired.
- `no_match` — it did not.

Conformance is defined entirely in these terms: for each fixture, an engine MUST produce
the fixture's declared `verdict`.

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
