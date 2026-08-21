// __BEACON_MANAGED_MARKER__
// Beacon endpoint telemetry extension for Pi (pi.dev).
// Managed by `beacon endpoint hooks install --harness pi`; local edits are replaced on repair.
//
// This file is a forwarder and nothing else. It reads what Pi hands each event, shapes it into the
// payload contract documented in cli/beacon-hooks/cmd/pi_event.go, and hands it to the Beacon hooks
// binary. Every decision that could be wrong -- what counts as a file edit, what gets redacted, how
// tokens map onto the schema -- is made in Go, because this file is the part that is hardest to test
// and the part that cannot be fixed without rewriting installed copies.

import { spawn } from "node:child_process"

// Argv, not a shell command string.
//
// The opencode plugin runs its command through `/bin/sh -lc`, which is fine there and would not be
// here: Pi installs through npm on Windows as readily as on macOS, and neither cmd.exe nor
// PowerShell parses a POSIX-quoted command line. Spawning argv directly removes the shell from the
// path entirely, so a profile directory containing a space, a `$`, or a backslash is carried as one
// argument instead of being re-split by whatever shell happened to be involved.
//
// The whole array literal is the substitution token, so this file parses both before and after the
// installer rewrites it -- which is what lets `bun --check` verify the template itself rather than
// only the rendered copy.
const beaconArgv: string[] = ["__BEACON_ARGV__"]

// A hook that hangs would hang the agent turn behind it, so every send is bounded. Two seconds is
// the same budget the opencode plugin uses; the work on the other side is one append to a local
// file.
const sendTimeoutMs = 2000

// The decision path gets a longer budget, and the margin is the point rather than caution.
//
// When a policy provider is configured, the hooks binary consults it with a 2s timeout of its own
// before answering. Reusing the 2s budget here would mean this side frequently gives up first: the
// spawn plus the provider's own work cannot fit in the same 2s the provider alone is allowed. A
// timeout on this side is treated as allow, so the visible effect would be a provider that denies
// and a tool that runs anyway -- an enforcement gap that looks like a flaky provider.
const decisionTimeoutMs = 6000

const debugEnabled = process.env.BEACON_PI_DEBUG === "1"

function debugLog(message: string, extra?: unknown): void {
  if (!debugEnabled) return
  // stderr, never stdout: Pi's print and JSON modes put machine-readable output on stdout, and a
  // debug line there would corrupt whatever is parsing it.
  try {
    process.stderr.write(`beacon-pi: ${message} ${extra === undefined ? "" : JSON.stringify(extra)}\n`)
  } catch {
    // Debug logging is best-effort by definition.
  }
}

// A decision the hooks binary returned. Only the deny case carries anything: an allow is an empty
// response, which is also what a failure of any kind produces.
type BeaconDecision = { block?: boolean; reason?: string } | undefined

// send hands one payload to the hooks binary and resolves when it is done.
//
// It never rejects. Telemetry that breaks the agent it is observing is worse than telemetry that
// misses an event, so a missing binary, a non-zero exit, and a timeout are all logged and swallowed.
//
// wantDecision makes it read the reply instead of discarding it. Passing it is what turns a
// fire-and-forget observation into a blocking question, so it is set for exactly one event.
function send(payload: Record<string, unknown>, wantDecision = false): Promise<BeaconDecision> {
  const testSender = (globalThis as Record<symbol, unknown>)[Symbol.for("beacon.pi.testSender")]
  if (typeof testSender === "function") {
    return Promise.resolve((testSender as (value: unknown, wantDecision: boolean) => unknown)(payload, wantDecision)).then(
      (value) => (wantDecision ? (value as BeaconDecision) : undefined),
      () => undefined,
    )
  }
  if (beaconArgv.length === 0) {
    debugLog("no beacon command configured")
    return Promise.resolve(undefined)
  }

  return new Promise<BeaconDecision>((resolve) => {
    let child: ReturnType<typeof spawn>
    try {
      child = spawn(beaconArgv[0], beaconArgv.slice(1), {
        stdio: ["pipe", wantDecision ? "pipe" : "ignore", "ignore"],
        windowsHide: true,
      })
    } catch (err) {
      debugLog("spawn failed", { error: String(err), type: payload.type })
      resolve(undefined)
      return
    }

    let stdout = ""
    if (wantDecision) {
      child.stdout?.setEncoding("utf8")
      child.stdout?.on("data", (chunk: string) => {
        stdout += chunk
      })
      child.stdout?.on("error", () => {})
    }

    // settle() is guarded because more than one of these paths can fire for the same child -- a
    // timeout followed by the exit it caused is the normal case -- and resolving twice would leave
    // the timer running for a process that is already gone.
    let settled = false
    const settle = (decision?: BeaconDecision) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      resolve(decision)
    }
    const timer = setTimeout(
      () => {
        debugLog("hook command timed out", { type: payload.type })
        child.kill()
        // No decision on timeout, which means allow. Blocking a tool because Beacon was slow would
        // make an observability tool into an outage.
        settle()
      },
      wantDecision ? decisionTimeoutMs : sendTimeoutMs,
    )
    // The timer must not be a reason for the process to stay alive: Pi exiting while a send is in
    // flight should not wait out the full timeout.
    timer.unref?.()

    child.on("error", (err) => {
      debugLog("hook command failed", { error: String(err), type: payload.type })
      settle()
    })
    child.on("close", (code) => {
      if (code !== 0) {
        debugLog("hook command exited non-zero", { code, type: payload.type })
        // A non-zero exit is not a decision. Treating it as one would let a broken install block
        // every tool call.
        settle()
        return
      }
      settle(wantDecision ? parseDecision(stdout, payload) : undefined)
    })
    // EPIPE if the child died before reading; already reported through the error handler above.
    child.stdin?.on("error", () => {})
    child.stdin?.end(JSON.stringify(payload))
  })
}

// parseDecision reads the hooks binary's reply.
//
// Only an explicit `block: true` is a deny. Unparseable output, an empty object, or any other shape
// yields no decision and the call proceeds -- the fail-open direction the policy seam is specified
// to take on every error path, kept identical on this side of it.
function parseDecision(stdout: string, payload: Record<string, unknown>): BeaconDecision {
  const text = stdout.trim()
  if (!text) return undefined
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    debugLog("hook command returned unparseable output", { type: payload.type })
    return undefined
  }
  if (!parsed || typeof parsed !== "object") return undefined
  const decision = parsed as { block?: unknown; reason?: unknown }
  if (decision.block !== true) return undefined
  return {
    block: true,
    reason: typeof decision.reason === "string" && decision.reason !== "" ? decision.reason : "Denied by Beacon policy",
  }
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === "string" && value !== "") return value
  }
  return ""
}

// sessionId is the one field this file guesses at.
//
// Pi's session id is documented as reachable through ctx.sessionManager, but the accessor is not
// named in the published extension docs, so several plausible spellings are tried and an empty
// result is accepted. Beacon records an event with no session.id rather than dropping it, which
// makes the failure mode "events arrive uncorrelated" instead of "no events arrive" -- recoverable
// from the log, and visible to whoever installed it.
function sessionId(event: Record<string, unknown>, ctx: Record<string, unknown>): string {
  const manager = ctx?.sessionManager as Record<string, unknown> | undefined
  const header = manager?.header as Record<string, unknown> | undefined
  const session = manager?.session as Record<string, unknown> | undefined
  return firstString(
    manager?.sessionId,
    manager?.sessionID,
    manager?.id,
    header?.id,
    session?.id,
    typeof manager?.getSessionId === "function" ? (manager.getSessionId as () => unknown)() : undefined,
    event?.sessionId,
    event?.session_id,
  )
}

function sessionFile(ctx: Record<string, unknown>): string {
  const manager = ctx?.sessionManager as Record<string, unknown> | undefined
  return firstString(manager?.path, manager?.file, manager?.sessionPath, manager?.sessionFile)
}

// modelName renders "<provider>/<model>", matching how every other Beacon runtime reports a model.
// Pi switches models mid-session, so this is read per event rather than captured once.
function modelName(value: unknown): string {
  if (typeof value === "string") return value
  if (!value || typeof value !== "object") return ""
  const model = value as Record<string, unknown>
  const provider = firstString(model.provider, model.providerId, model.providerID)
  const name = firstString(model.id, model.modelId, model.modelID, model.model, model.name)
  if (provider && name) return `${provider}/${name}`
  return name
}

// contentText flattens Pi's content blocks, keeping only the block types asked for.
//
// Pi delivers message and tool-result content as an array of typed blocks, and a plain String() of
// that array yields "[object Object]" -- which would be recorded as the tool's output and hashed as
// retained content, describing nothing.
function contentText(content: unknown, types?: string[]): string {
  if (typeof content === "string") return types ? "" : content
  if (!Array.isArray(content)) return ""
  const parts: string[] = []
  for (const block of content) {
    if (typeof block === "string") {
      if (!types) parts.push(block)
      continue
    }
    if (!block || typeof block !== "object") continue
    const typed = block as Record<string, unknown>
    const type = firstString(typed.type)
    if (types && !types.includes(type)) continue
    const text = firstString(typed.text, typed.content, typed.thinking, typed.reasoning)
    if (text) parts.push(text)
  }
  return parts.join("\n")
}

function toRecord(value: unknown): Record<string, unknown> | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined
  return value as Record<string, unknown>
}

export default function beaconEndpointExtension(pi: {
  on: (event: string, handler: (event: any, ctx: any) => unknown) => unknown
}): void {
  // Every payload carries the same context. Built per event because cwd and the active model can
  // both change during a session.
  const base = (type: string, event: Record<string, unknown>, ctx: Record<string, unknown>) => {
    const payload: Record<string, unknown> = { type }
    const id = sessionId(event, ctx)
    if (id) payload.session_id = id
    const cwd = firstString(ctx?.cwd)
    if (cwd) payload.cwd = cwd
    const model = modelName(ctx?.model)
    if (model) payload.model = model
    const file = sessionFile(ctx)
    if (file) payload.session_file = file
    const reason = firstString(event?.reason)
    if (reason) payload.reason = reason
    return payload
  }

  // Handlers return nothing, with exactly one deliberate exception.
  //
  // Pi reads a handler's return value as a directive: a truthy result from tool_call blocks the
  // call, and one from tool_result or input rewrites what the agent sees. An extension that leaked
  // its send() result would silently change the agent's behavior, which is exactly the failure a
  // telemetry tool must not have. `void` on each call is what makes that impossible rather than
  // merely unlikely.
  //
  // The exception is tool_call, which returns a deny when -- and only when -- an external policy
  // provider said so. It is the one handler where a return value is the intent rather than an
  // accident, and it is spelled out at the handler itself.
  pi.on("session_start", (event, ctx) => {
    void send(base("session_start", event, ctx))
  })

  pi.on("session_shutdown", async (event, ctx) => {
    // Awaited, unlike the rest: this is the last event of the session, and Pi may exit as soon as
    // the handler resolves. Firing and forgetting here loses the session-end event on a fast exit.
    await send(base("session_shutdown", event, ctx))
  })

  pi.on("input", (event, ctx) => {
    const prompt = firstString(event?.text, event?.input, event?.value, event?.prompt)
    if (!prompt) return
    void send({ ...base("input", event, ctx), prompt })
  })

  // tool_call is the one handler that awaits its send and the one that can return a value.
  //
  // It is Pi's only blockable pre-execution event, so it is where Beacon's policy seam can be
  // honored with full fidelity: the hooks binary consults the configured provider and answers with
  // Pi's own {block, reason} shape, which is returned here unchanged. With no provider configured --
  // the default for the open build -- the reply is empty, this returns undefined, and the behavior
  // is identical to every other handler.
  //
  // Awaiting adds the provider's latency to the tool call, which is inherent to asking a question
  // before an action rather than reporting it afterwards. Every failure path resolves to no
  // decision, so a slow or broken provider costs latency and never blocks a call.
  pi.on("tool_call", async (event, ctx) => {
    const decision = await send(
      {
        ...base("tool_call", event, ctx),
        tool_name: firstString(event?.toolName, event?.tool_name, event?.name),
        tool_call_id: firstString(event?.toolCallId, event?.toolCallID, event?.tool_call_id),
        // event.input is mutable and Pi may hand the same object to other extensions after this
        // one. A shallow copy keeps the payload describing the arguments as they were at this
        // moment.
        tool_input: { ...(toRecord(event?.input) ?? {}) },
      },
      true,
    )
    if (decision?.block === true) {
      return { block: true, reason: decision.reason }
    }
    return undefined
  })

  pi.on("tool_result", (event, ctx) => {
    const details = toRecord(event?.details)
    const response: Record<string, unknown> = {}
    const output = contentText(event?.content)
    if (output) response.output = output
    if (details) {
      response.details = details
      // Hoisted to the top level because that is where Beacon reads a command's exit code from; a
      // nested one would leave every shell result reporting no status at all.
      const exitCode = details.exitCode ?? details.exit_code ?? details.exit ?? details.status
      if (typeof exitCode === "number") response.exit_code = exitCode
    }
    void send({
      ...base("tool_result", event, ctx),
      tool_name: firstString(event?.toolName, event?.tool_name, event?.name),
      tool_call_id: firstString(event?.toolCallId, event?.toolCallID, event?.tool_call_id),
      tool_input: { ...(toRecord(event?.input) ?? {}) },
      tool_response: response,
      is_error: event?.isError === true,
    })
  })

  pi.on("message_end", (event, ctx) => {
    const message = toRecord(event?.message) ?? toRecord(event)
    if (!message) return
    // Only assistant messages carry usage and reasoning. A user message here would produce a
    // response event with no model and no tokens, duplicating what the input event already said.
    if (firstString(message.role) !== "assistant") return

    const payload: Record<string, unknown> = { ...base("message_end", event, ctx) }
    const model = modelName({ provider: message.provider, id: message.model }) || modelName(ctx?.model)
    if (model) payload.model = model
    // Pi's usage object is forwarded as-is. Beacon maps its members onto gen_ai.usage and drops
    // what the schema has no member for; doing that translation here would put schema decisions in
    // the layer that cannot be updated without a reinstall.
    const usage = toRecord(message.usage)
    if (usage) payload.usage = usage
    const finish = firstString(message.stopReason, message.finishReason)
    if (finish) payload.finish_reason = finish
    const id = firstString(message.id)
    if (id) payload.message_id = id
    const reasoning = contentText(message.content, ["thinking", "reasoning"])
    if (reasoning) payload.reasoning = reasoning

    void send(payload)
  })

  pi.on("agent_end", (event, ctx) => {
    void send(base("agent_end", event, ctx))
  })
}
