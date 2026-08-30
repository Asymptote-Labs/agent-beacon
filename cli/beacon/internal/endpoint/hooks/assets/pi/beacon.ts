// __BEACON_MANAGED_MARKER__
// Beacon endpoint telemetry extension for the Pi coding agent and its distributions.
// Managed by beacon endpoint hooks install --harness pi (or --harness prime).

// One file serves Pi and Prime Agent because they are one extension API.
//
// Prime Agent (Prime Intellect) ships @earendil-works/pi-coding-agent with a rebranded config
// directory: same `on(event, handler)` surface, same event objects, same default export contract,
// a different `~/.prime/agent` root. Beacon therefore installs the same source into both, rendered
// twice with a different runtime name -- rather than keeping a second copy that would drift from
// this one the first time either side is fixed.
//
// What the runtime name decides is deliberately small: which hook subcommand the argv below
// invokes, which events are subscribed to, and which debug variable turns on logging. Everything
// else -- serialization, spawning, identity lifting -- is identical because the runtimes are.

// The argv Beacon's installer writes, as an array rather than a command line.
//
// An argv array is passed straight to the OS with no shell in between, which sidesteps quoting
// entirely -- the same reason the Cline plugin uses one. Pi runs as a Bun-compiled binary or under
// Node depending on how it was installed, and on Windows as readily as on macOS, so there is no one
// shell whose quoting rules would hold.
const beaconArgv: string[] = ["__BEACON_ARGV__"]

// Which distribution this rendered copy was installed for: "pi" or "prime".
//
// Substituted by the installer, like the argv above, rather than sniffed at runtime. A file that
// guessed -- from the directory it sits in, or from an environment variable -- would subscribe to
// the wrong event set on a machine that runs both, and the guess would be wrong exactly when both
// runtimes are present, which is the case the field exists to distinguish.
const beaconRuntime = "__BEACON_RUNTIME__"

// Debug logging is per runtime -- BEACON_PI_DEBUG or BEACON_PRIME_DEBUG -- so turning it on for one
// does not flood the terminal of the other on a machine running both.
const debugEnabled = process.env[`BEACON_${beaconRuntime.toUpperCase()}_DEBUG`] === "1"

// A send that has not finished in this long is abandoned. Both runtimes await event handlers before
// continuing the agent loop, so this is the worst case Beacon can add to a tool call.
const sendTimeoutMs = 2000

// How deep into an event the serializer will walk before giving up.
const maxDepth = 8

// The runtime's own event objects, narrowed to the fields Beacon reads.
//
// Declared here rather than imported from @earendil-works/pi-coding-agent on purpose. The installed
// file sits in ~/.pi/agent/extensions -- or ~/.prime/agent/extensions -- with no node_modules beside
// it, and the host loads it through jiti, which strips types without resolving them. A value import
// would fail at load; a type import would resolve for nobody. Local structural types cost the
// compile-time link to the vendor definitions and buy a file that loads wherever the runtime does.
type PiEvent = Record<string, unknown> & { type: string }

interface PiSessionManager {
  getSessionId?: () => string
  getCwd?: () => string
}

interface PiContext {
  cwd?: string
  sessionManager?: PiSessionManager
  model?: { id?: string; name?: string; provider?: string } | undefined
  mode?: string
}

// The events Beacon subscribes to, and nothing else.
//
// Both runtimes publish roughly thirty event types, most of them provider-request and TUI-rendering
// internals that describe no agent action. Subscribing to all of them would fill the runtime log
// with rows no query asks for -- the same reason the Cline mapper drops unrecognized stages -- and
// would put Beacon in the path of every streaming token update.
//
// `tool_call` and `tool_result` are the pair that carries tool activity: the first names the tool
// and its arguments before it runs, the second carries the outcome. `user_bash` is a command the
// human ran with the `!` prefix, which no tool event covers.
//
// Deliberately absent from both lists: any approval event. The `tool_call` handler can block a
// call, but that is an extension deciding, not an operator being asked -- neither runtime exposes
// an operator approval decision through this API. Synthesizing an approval from a pre-tool
// notification would put a decision nobody made into the log, so Beacon records these as tool
// activity and leaves approval telemetry empty here, exactly as it does for Cline.
//
// These strings are the contract with the `pi-event` and `prime-event` mappers in the hook adapter.
// A typo on either side produces no telemetry rather than an error, so both sides pin the list.
const sharedEvents = [
  "session_start",
  "session_shutdown",
  "input",
  "tool_call",
  "tool_result",
  "user_bash",
  "message_end",
] as const

// Prime Agent adds two events that Pi's build does not act on, and both describe something Beacon
// has no other way to see.
//
// `session_compact` says the conversation history was rewritten. Reading a Prime log without it,
// the prompts and tool calls before a compaction look like they are still in context when they are
// not, which changes what a reviewer concludes the agent was working from.
//
// `refine_complete` says the agent edited its own harness -- the durable prompts, memories, and
// skill descriptions it will start every future session with. That is the self-improving loop the
// product is built around, and it is the one action in the runtime that outlives the session that
// took it, so an endpoint telemetry agent that cannot see it is missing the change with the longest
// blast radius.
const primeOnlyEvents = ["session_compact", "refine_complete"] as const

const eventsByRuntime: Record<string, readonly string[]> = {
  pi: sharedEvents,
  prime: [...sharedEvents, ...primeOnlyEvents],
}

// An unrecognized runtime name still observes the shared events rather than nothing. The installer
// only ever writes a name this map knows, so reaching the fallback means the file was edited by
// hand -- and a hand-edited file that reports the common events is a better failure than one that
// silently reports none.
function eventsFor(runtime: string): readonly string[] {
  return eventsByRuntime[runtime] ?? sharedEvents
}

function debugLog(message: string, extra?: unknown) {
  if (!debugEnabled) return
  try {
    // eslint-disable-next-line no-console
    console.error(`[beacon-${beaconRuntime}]`, message, extra ?? "")
  } catch {
    // Debug logging must stay best-effort.
  }
}

// safeClone turns a Pi event into something JSON.stringify accepts.
//
// Pi's events carry live objects: an AbortSignal on the session events, AgentMessage arrays holding
// class instances, and tool inputs that are mutable by design. JSON.stringify throws on a circular
// reference and on a bigint, and silently flattens an Error to {}, so each is handled here rather
// than losing the whole payload to one bad field.
//
// Cycles are tracked along the current path and released on the way out, so an object that legibly
// appears twice -- the same file path referenced from two places in one edit -- is kept both times.
// A WeakSet that never released would drop the second occurrence as if it were a cycle.
function safeClone(value: unknown, depth = 0, seen = new WeakSet<object>()): unknown {
  if (value === null) return null
  const kind = typeof value
  if (kind === "function" || kind === "symbol" || kind === "undefined") return undefined
  if (kind === "bigint") return String(value)
  if (kind !== "object") return value
  if (depth >= maxDepth) return undefined

  const object = value as object
  if (seen.has(object)) return undefined
  if (value instanceof Error) return { name: value.name, message: value.message }
  if (value instanceof Date) return value.toISOString()

  seen.add(object)
  try {
    if (Array.isArray(value)) {
      return value.map((item) => safeClone(item, depth + 1, seen))
    }
    const out: Record<string, unknown> = {}
    for (const [key, nested] of Object.entries(value as Record<string, unknown>)) {
      const cloned = safeClone(nested, depth + 1, seen)
      if (cloned !== undefined) out[key] = cloned
    }
    return out
  } finally {
    seen.delete(object)
  }
}

async function sendToBeacon(payload: Record<string, unknown>): Promise<void> {
  // Flattened before anything else sees it, so every consumer of this function is handed plain
  // data rather than a live runtime event with cycles and functions still attached.
  const safe = safeClone(payload)

  const testSender = (globalThis as Record<symbol, unknown>)[Symbol.for("beacon.extension.testSender")]
  if (typeof testSender === "function") {
    await (testSender as (value: unknown) => unknown)(safe)
    return
  }

  let body: string
  try {
    body = JSON.stringify(safe)
  } catch (err) {
    debugLog("payload could not be serialized", err)
    return
  }
  if (!body) return

  try {
    // Imported lazily so a host without node:child_process fails to send rather than failing to
    // load: a throwing import at module scope would surface to the user as a broken extension, and
    // the host reports an extension that throws at load time rather than continuing without it.
    const { spawn } = await import("node:child_process")
    await new Promise<void>((resolve) => {
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
        debugLog("hook binary timed out", { type: payload?.type })
        finish()
      }, sendTimeoutMs)
      // An unreferenced timer cannot keep the host alive at exit waiting on Beacon telemetry.
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
      // unhandled error event in the host process. Beacon telemetry must never do that.
      child.stdin?.on("error", () => {})
      try {
        child.stdin?.end(body)
      } catch (err) {
        debugLog("payload could not be written", err)
      }
    })
  } catch (err) {
    debugLog("send failed", err)
    // Beacon telemetry must never interrupt the agent run.
  }
}

// identity lifts the session and workspace fields Beacon needs to the top level of the envelope.
//
// Pi keeps them on the handler context rather than on the event, and behind accessor functions
// rather than as properties, so they have to be read per event: a session id captured once at
// extension load would be wrong after `/new`, a resume, or a fork, all of which replace the session
// without reloading the extension.
//
// Each accessor is called defensively. `getSessionId` and `getCwd` are documented members of the
// read-only session manager, but an extension that loses its session id loses the field that groups
// every event of a run, so a throwing accessor costs that field rather than the event.
function identity(ctx: PiContext | undefined): Record<string, unknown> {
  const fields: Record<string, unknown> = {}
  if (!ctx) return fields
  const manager = ctx.sessionManager
  try {
    const sessionId = manager?.getSessionId?.()
    if (sessionId) fields.sessionId = sessionId
  } catch (err) {
    debugLog("session id could not be read", err)
  }
  let cwd = ctx.cwd
  if (!cwd) {
    try {
      cwd = manager?.getCwd?.()
    } catch (err) {
      debugLog("cwd could not be read", err)
    }
  }
  if (cwd) fields.cwd = cwd
  const model = ctx.model
  if (model) {
    // Pi's Model carries an id and a provider separately. Joined here so the envelope's `model` is
    // one string, matching how every other Beacon collection path reports it.
    const name = model.id || model.name
    if (name) fields.model = model.provider ? `${model.provider}/${name}` : name
  }
  if (ctx.mode) fields.piMode = ctx.mode
  return fields
}

type PiExtensionAPI = {
  on: (event: string, handler: (event: PiEvent, ctx: PiContext) => Promise<void> | void) => void
}

// createBeaconExtension is exported for tests; the runtime loads the default export below.
//
// The runtime name is a parameter with the installed value as its default, so a test can build the
// Pi and the Prime subscription without rendering the template first, and the rendered file still
// needs no argument.
export function createBeaconExtension(runtime: string = beaconRuntime) {
  const subscribedEvents = eventsFor(runtime)
  const forward = async (event: PiEvent, ctx: PiContext) => {
    try {
      await sendToBeacon({ ...identity(ctx), ...event })
    } catch (err) {
      // Unreachable in practice: sendToBeacon swallows its own failures. Kept because a handler
      // that rejects is a handler that can break the run it was observing.
      debugLog("handler failed", err)
    }
  }

  return {
    register(pi: PiExtensionAPI) {
      for (const name of subscribedEvents) {
        // Every handler returns undefined. Beacon observes; it never blocks a tool call, rewrites a
        // prompt, transforms input, or replaces a message. The host reads a returned object as a
        // request to change behavior -- `{ block: true }` on tool_call, `{ action: "transform" }`
        // on input -- so returning nothing is what keeps this extension an observer. Enforcement
        // lives behind the policy provider seam in the hook adapter, not here.
        pi.on(name, forward)
      }
    },
    // Exposed so a test can assert the subscription list without a live host instance.
    subscribedEvents: [...subscribedEvents],
  }
}

export default function beaconExtension(pi: PiExtensionAPI) {
  createBeaconExtension().register(pi)
}
