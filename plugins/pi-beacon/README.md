# Beacon endpoint extension for Pi

Beacon's telemetry adapter for [Pi](https://pi.dev), a terminal coding agent.

Pi has no hooks configuration file and no OpenTelemetry support, so neither of Beacon's other two
integration shapes applies to it. Its documented observation surface is the TypeScript extension
API, so this is what Beacon installs: one extension that forwards the events it subscribes to.

## What it does

Subscribes to `session_start`, `session_shutdown`, `input`, `tool_call`, `tool_result`,
`message_end` and `agent_end`, and hands each one to the `beacon-hooks` binary as JSON on stdin.
The payload contract is documented in `cli/beacon-hooks/cmd/pi_event.go`, which is also where every
mapping decision lives — what counts as a file edit, what gets redacted, how Pi's usage object maps
onto `gen_ai.usage`. This file deliberately makes none of those decisions: it is the layer that
cannot be fixed without rewriting installed copies, so it stays a forwarder.

Local-only, like the rest of Beacon's endpoint execution. The extension makes no network calls; it
spawns a local binary that appends to a local log.

## Policy enforcement

Pi's `tool_call` is its only blockable pre-execution event, which is what lets Beacon's optional
policy seam be honored with full fidelity here. When `BEACON_POLICY_PROVIDER` names an executable,
the hooks binary consults it and answers a denied `tool_call` with Pi's own `{block, reason}` shape,
which the handler returns unchanged — no translation in this layer. The denial is recorded as
`approval.denied` with `policy.enforcement=enforce`, and no `tool.invoked` event is written, because
the tool never ran.

With no provider configured — the default for the open build — the reply is empty, the handler
returns `undefined`, and `tool_call` behaves exactly like every observation handler. Every failure
path (timeout, non-zero exit, unparseable output, a reply that is not `block: true`) is an allow.

## Properties worth preserving

- **Observation handlers return nothing.** Pi reads a handler's return value as a directive: truthy
  from `tool_call` blocks the call, truthy from `input` or `tool_result` rewrites what the agent
  sees. Nothing may leak a value into that channel by accident, which is why every observation
  handler `void`s its send. The single intentional exception is the policy deny below.
- **Failures are swallowed.** A missing binary, a non-zero exit, or a timeout is logged (with
  `BEACON_PI_DEBUG=1`) and dropped. Telemetry that breaks the agent it observes is worse than
  telemetry that misses an event.
- **argv, not a shell string.** The command is spawned as an argv array with no shell, so paths
  containing spaces or backslashes work on Windows as well as POSIX.
- **`session_shutdown` is awaited.** It is the last event of the session and Pi may exit as soon as
  the handler resolves.
- **`tool_call` is the only handler that may return a value**, and only a policy deny. Its response
  timeout is deliberately longer than the observation timeout: the hooks binary gives the provider
  2s of its own, so reusing that budget here would make this side give up first and allow calls the
  provider denied.

## Known gap

Pi's session id is read through `ctx.sessionManager`, but the published extension documentation does
not name the accessor. `sessionId()` tries several plausible spellings and accepts an empty result:
Beacon records an event without `session.id` rather than dropping it, so an unknown shape degrades
to uncorrelated telemetry instead of silence. Confirm the real accessor against a live Pi session
and narrow the list; `beacon-hooks` needs no change either way.

## Development

```bash
bun test              # unit tests, via an injected sender -- no process is spawned
bun run check         # parse the template and verify the embedded Go copy is in sync
bun run sync          # copy src/beacon.ts into the Go installer's embedded assets
```

`src/beacon.ts` is the source of truth. `cli/beacon/internal/endpoint/hooks/assets/pi/beacon.ts` is
a checked-in copy that Go embeds, and a test fails if the two drift. The `__BEACON_ARGV__` and
`__BEACON_MANAGED_MARKER__` placeholders are substituted at install time; the installer refuses to
write a file that still contains an unresolved `__BEACON_` token.
