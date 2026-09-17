// __BEACON_MANAGED_MARKER__
// Beacon endpoint telemetry plugin for OpenClaw Gateway.
// Managed by beacon endpoint hooks install --harness openclaw.

// The argv Beacon's installer writes, as an array rather than a command line.
//
// An argv array is passed straight to the OS with no shell in between, which sidesteps quoting
// entirely -- the same reason the Cline plugin and the Pi-family extensions use one. OpenClaw runs
// as a long-lived gateway on macOS, Linux and Windows alike, so there is no single shell whose
// quoting rules would hold.
const beaconArgv = ["__BEACON_ARGV__"]

const debugEnabled = process.env.BEACON_OPENCLAW_DEBUG === "1"

// A send that has not finished in this long is abandoned.
//
// Well under OpenClaw's 15 s default hook budget on purpose, and the number only matters for the
// observe hooks: `before_tool_call` never waits at all (see dispatch below).
const sendTimeoutMs = 2000

// How deep into an event the serializer will walk before giving up.
const maxDepth = 8

// The hooks this plugin registers, and nothing else.
//
// OpenClaw publishes forty-one typed hooks. These ten are the ones that describe what the agent
// did; the rest describe how the gateway got its work done. The full reasoning for each exclusion
// lives beside supportedOpenClawHooks() in cli/beacon-hooks/cmd/openclaw_event.go, which is the
// mapper half of this contract -- a name that disagrees between the two files produces no
// telemetry rather than an error, so both sides pin the list and a test asserts they match.
//
// Three exclusions are worth repeating here, because they are about what this plugin is allowed to
// be rather than about payload volume. `before_prompt_build` and `agent_turn_prepare` are prompt
// *injection* hooks; `inbound_claim` and the claim-kind delivery hooks can swallow a message
// outright; and `skill_proposal_evaluate` makes its handler a voting evaluator on whether a skill
// may be installed. Beacon observes. Registering any of them would ask OpenClaw for a power Beacon
// must not hold, and would do so silently.
const subscribedHooks = [
  "session_start",
  "session_end",
  "message_received",
  "before_tool_call",
  "after_tool_call",
  "llm_output",
  "before_compaction",
  "after_compaction",
  "subagent_spawned",
  "subagent_ended",
]

// The hooks whose handler must never make OpenClaw wait.
//
// `before_tool_call` is registered fail-closed by OpenClaw itself: its default failure policy for
// this hook is "fail-closed", so a handler that throws or exceeds its timeout *blocks the tool
// call*. Beacon telemetry must never be able to stop an agent from working, so this handler
// dispatches its send and returns synchronously, without awaiting and without a promise for
// OpenClaw to wait on. The gateway is a long-lived process, so an unawaited child still runs to
// completion.
//
// The cost is ordering: a `tool.invoked` row can land in the log after the `tool.completed` row
// for the same call. That is acceptable because ordering was never the join -- `gen_ai.tool.call.id`
// is, and both halves carry it.
const nonBlockingHooks = new Set(["before_tool_call"])

// How many in-flight tool calls the apply_patch enrichment below will remember at once.
//
// Bounded because the map is cleared by `after_tool_call`, and a call that never produces one --
// an aborted run, a gateway restart mid-tool -- would otherwise leave an entry behind for the life
// of the process. A gateway fans several conversations into one process, so this is set well above
// a single agent's working set and is still only a few kilobytes at worst.
const maxPendingToolCalls = 256

// The destination paths OpenClaw derived for a proposed tool call, keyed by its own tool call id.
//
// This exists for one reason: `before_tool_call` carries `derivedPaths` and `after_tool_call` does
// not. `apply_patch` takes the whole patch envelope as a single string argument, so without those
// paths a completed patch is a tool call with a blob attached and no file rows at all -- and every
// file-scoped detection Beacon ships matches on `file.path`.
//
// The join is OpenClaw's own call id rather than a timestamp or a guess, and both halves fire in
// this same process, so remembering the value until the call completes is exact.
const pendingDerivedPaths = new Map()

function rememberDerivedPaths(event) {
  const id = typeof event?.toolCallId === "string" ? event.toolCallId : ""
  if (!id || !Array.isArray(event.derivedPaths) || event.derivedPaths.length === 0) return
  // Re-inserting moves the entry to the end of the Map's insertion order, which is what makes the
  // eviction below drop the genuinely oldest call rather than the least recently seen one.
  pendingDerivedPaths.delete(id)
  pendingDerivedPaths.set(id, event.derivedPaths)
  while (pendingDerivedPaths.size > maxPendingToolCalls) {
    const oldest = pendingDerivedPaths.keys().next()
    if (oldest.done) break
    pendingDerivedPaths.delete(oldest.value)
  }
}

// takeDerivedPaths returns and forgets the paths remembered for a completed call.
//
// Absent rather than guessed when the call is unknown: a patch event enriched with some other
// call's paths would be worse than one with none, because it would read as evidence that those
// files changed.
function takeDerivedPaths(event) {
  const id = typeof event?.toolCallId === "string" ? event.toolCallId : ""
  if (!id) return undefined
  const paths = pendingDerivedPaths.get(id)
  pendingDerivedPaths.delete(id)
  return paths
}

function debugLog(message, extra) {
  if (!debugEnabled) return
  try {
    console.error("[beacon-openclaw]", message, extra ?? "")
  } catch {
    // Debug logging must stay best-effort.
  }
}

// safeClone turns an OpenClaw hook payload into something JSON.stringify accepts.
//
// The payloads carry live objects: an AbortSignal on the tool context, provider message objects on
// `llm_output`, and tool params that are mutable by design. JSON.stringify throws on a circular
// reference and on a bigint, and silently flattens an Error to {}, so each is handled here rather
// than losing the whole payload to one bad field.
//
// Cycles are tracked along the current path and released on the way out, so an object that legibly
// appears twice -- the same file path referenced from two places in one patch -- is kept both
// times. A WeakSet that never released would drop the second occurrence as if it were a cycle.
function safeClone(value, depth = 0, seen = new WeakSet()) {
  if (value === null) return null
  const kind = typeof value
  if (kind === "function" || kind === "symbol" || kind === "undefined") return undefined
  if (kind === "bigint") return String(value)
  if (kind !== "object") return value
  if (depth >= maxDepth) return undefined

  if (seen.has(value)) return undefined
  if (value instanceof Error) return { name: value.name, message: value.message }
  if (value instanceof Date) return value.toISOString()

  seen.add(value)
  try {
    if (Array.isArray(value)) {
      return value.map((item) => safeClone(item, depth + 1, seen))
    }
    const out = {}
    for (const [key, nested] of Object.entries(value)) {
      const cloned = safeClone(nested, depth + 1, seen)
      if (cloned !== undefined) out[key] = cloned
    }
    return out
  } finally {
    seen.delete(value)
  }
}

// sendToBeacon spawns the hook binary and writes one envelope to its stdin.
//
// Returns a promise that always resolves. Nothing in this file rejects: a handler that rejects is
// a handler that can break the run it was observing, and on `before_tool_call` it would block the
// tool call outright.
function sendToBeacon(payload) {
  try {
    return dispatch(payload)
  } catch (err) {
    // A synchronous throw would escape the handler, and on `before_tool_call` that blocks the
    // tool call. The body below is written not to throw; this is the guard that makes "not to
    // throw" a property of the function rather than a property of its current shape.
    debugLog("send failed", err)
    return Promise.resolve()
  }
}

function dispatch(payload) {
  // Flattened before anything else sees it, so every consumer is handed plain data rather than a
  // live payload with cycles and functions still attached.
  const safe = safeClone(payload)

  const testSender = globalThis[Symbol.for("beacon.openclaw.testSender")]
  if (typeof testSender === "function") {
    return Promise.resolve(testSender(safe))
  }

  let body
  try {
    body = JSON.stringify(safe)
  } catch (err) {
    debugLog("payload could not be serialized", err)
    return Promise.resolve()
  }
  if (!body) return Promise.resolve()

  // Imported lazily so a host without node:child_process fails to send rather than failing to
  // load: a throwing import at module scope would surface to the operator as a broken plugin, and
  // OpenClaw disables a plugin that throws while loading rather than continuing without it.
  return import("node:child_process")
    .then(
      ({ spawn }) =>
        new Promise((resolve) => {
          let settled = false
          const finish = () => {
            if (settled) return
            settled = true
            resolve()
          }
          let child
          try {
            child = spawn(beaconArgv[0], beaconArgv.slice(1), {
              stdio: ["pipe", "ignore", "ignore"],
              windowsHide: true,
            })
          } catch (err) {
            debugLog("hook binary could not be spawned", err)
            finish()
            return
          }
          const timer = setTimeout(() => {
            try {
              child.kill()
            } catch {
              // Already gone.
            }
            debugLog("hook binary timed out", { hook: payload?.hook })
            finish()
          }, sendTimeoutMs)
          // An unreferenced timer cannot keep the gateway alive at exit waiting on Beacon
          // telemetry.
          if (typeof timer.unref === "function") timer.unref()
          const done = () => {
            clearTimeout(timer)
            finish()
          }
          child.on("error", (err) => {
            debugLog("hook binary failed", err)
            done()
          })
          child.on("close", done)
          // Without this, a hook binary that exits before reading stdin turns an EPIPE into an
          // unhandled error event in the gateway process. Beacon telemetry must never do that.
          child.stdin?.on("error", () => {})
          try {
            child.stdin?.end(body)
          } catch (err) {
            debugLog("payload could not be written", err)
          }
        }),
    )
    .catch((err) => {
      debugLog("send failed", err)
    })
}

// identity lifts the session and workspace fields Beacon needs out of the hook context.
//
// Read per event rather than captured once at load, because a gateway process serves many
// conversations at once: a session id cached at registration would label every later event with
// whichever conversation happened to be first.
//
// `sessionKey` is carried alongside `sessionId` rather than instead of it. The two are not
// interchangeable -- `sessionKey` is the canonical conversation, stable across the session-id
// rotation a compaction causes, while `sessionId` is one generation of it. Beacon groups events by
// the session and keeps the conversation key beside them.
//
// `workspaceDir` is absent for a chat turn that never touched a repository, and is left absent
// rather than defaulted to the gateway's own working directory, which would attribute the work to
// wherever the daemon happened to be started.
function identity(ctx) {
  const fields = {}
  if (!ctx || typeof ctx !== "object") return fields
  for (const [target, source] of [
    ["sessionId", "sessionId"],
    ["sessionKey", "sessionKey"],
    ["runId", "runId"],
    ["agentId", "agentId"],
    ["channel", "channel"],
    ["accountId", "accountId"],
    ["trigger", "trigger"],
  ]) {
    const value = ctx[source]
    if (typeof value === "string" && value) fields[target] = value
  }
  if (typeof ctx.workspaceDir === "string" && ctx.workspaceDir) fields.cwd = ctx.workspaceDir
  // OpenClaw keeps the provider and the model id apart on the hook context. Joined here into the
  // one provider-prefixed string every other Beacon collection path reports, which the writer
  // then splits back into the canonical model name and `gen_ai.provider.name`. Sending the bare
  // model id would leave the provider unrecorded, and a gateway routes one model through several.
  if (typeof ctx.modelId === "string" && ctx.modelId) {
    fields.model =
      typeof ctx.modelProviderId === "string" && ctx.modelProviderId
        ? `${ctx.modelProviderId}/${ctx.modelId}`
        : ctx.modelId
  }
  return fields
}

// buildEnvelope assembles what the hook binary reads from stdin.
//
// The hook event stays nested under `event` rather than being spread onto the envelope. OpenClaw
// hands a handler `(event, ctx)` as two objects whose field names overlap -- `runId`, `sessionKey`
// and `sessionId` are on both -- and `before_tool_call` carries a `params` object whose keys belong
// to the model rather than to OpenClaw. A flattened envelope would let a tool argument named
// `sessionId` overwrite the session the event belongs to. One level of nesting makes that
// impossible, and the mapper is written against it.
function buildEnvelope(hook, event, ctx) {
  const envelope = identity(ctx)
  envelope.hook = hook
  // A hook whose event carries its own session identity wins over the context's: `session_start`,
  // `session_end` and `message_received` each name the session they are about, and the context is
  // the more general answer.
  if (event && typeof event === "object") {
    for (const key of ["sessionId", "sessionKey", "runId"]) {
      const value = event[key]
      if (typeof value === "string" && value) envelope[key] = value
    }
  }
  envelope.event = event ?? {}
  return envelope
}

// createBeaconPlugin is exported for tests; OpenClaw loads the default export below.
export function createBeaconPlugin() {
  const handlerFor = (hook) => (event, ctx) => {
    let envelope
    try {
      envelope = buildEnvelope(hook, event, ctx)

      if (hook === "before_tool_call") {
        rememberDerivedPaths(event)
      } else if (hook === "after_tool_call") {
        // OpenClaw does not repeat `derivedPaths` on the completion, so the value remembered from
        // the proposal is attached here under the same key the mapper already reads.
        const derived = takeDerivedPaths(event)
        if (derived !== undefined && envelope.event && typeof envelope.event === "object") {
          envelope.event = { ...envelope.event, derivedPaths: derived }
        }
      } else if (hook === "session_end") {
        // A session ending abandons any call still in flight for it. Clearing keeps those paths
        // from being joined to an unrelated call id in a later session.
        pendingDerivedPaths.clear()
      }
    } catch (err) {
      // Unreachable in practice, and caught anyway: a throw here on `before_tool_call` would block
      // the tool call, because OpenClaw registers that hook fail-closed.
      debugLog("envelope could not be built", err)
      return undefined
    }

    if (nonBlockingHooks.has(hook)) {
      // Dispatched without awaiting and without returning a promise. See nonBlockingHooks.
      void sendToBeacon(envelope)
      return undefined
    }
    // Returns a promise that always resolves to undefined. OpenClaw reads a returned object on its
    // modify-kind hooks as a request to change behavior; returning nothing is what keeps this
    // plugin an observer. Enforcement lives behind the policy provider seam in the hook adapter,
    // not here.
    return sendToBeacon(envelope).then(() => undefined)
  }

  return {
    register(api) {
      for (const hook of subscribedHooks) {
        api.on(hook, handlerFor(hook))
      }
    },
    // Exposed so a test can assert the subscription list without a live gateway.
    subscribedHooks: [...subscribedHooks],
  }
}

export default {
  id: "beacon-endpoint",
  name: "Beacon Endpoint Telemetry",
  description: "Records OpenClaw agent activity to the local Beacon endpoint log.",
  register(api) {
    createBeaconPlugin().register(api)
  },
}
