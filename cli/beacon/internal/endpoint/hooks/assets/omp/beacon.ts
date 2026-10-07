// __BEACON_MANAGED_MARKER__
// Beacon endpoint telemetry extension for Oh My Pi.
// Managed by beacon endpoint hooks install --harness omp.

// The argv Beacon's installer writes, as an array rather than a command line.
//
// An argv array is passed straight to the OS with no shell in between, which sidesteps quoting
// entirely -- the same reason the Cline plugin and the Pi extension use one. Oh My Pi ships as a
// compiled binary on macOS, Linux and Windows alike, so there is no one shell whose quoting rules
// would hold.
const beaconArgv: string[] = ["__BEACON_ARGV__"]

const debugEnabled = process.env.BEACON_OMP_DEBUG === "1"

// A send that has not finished in this long is abandoned. Oh My Pi awaits extension handlers before
// continuing, so this is the worst case Beacon can add to a tool call.
const sendTimeoutMs = 2000

// How deep into an event the serializer will walk before giving up.
const maxDepth = 8

// Oh My Pi's own event objects, narrowed to the fields Beacon reads.
//
// Declared here rather than imported from @oh-my-pi/pi-coding-agent on purpose. The installed file
// sits in ~/.omp/agent/extensions with no node_modules beside it, and the loader imports it
// directly, stripping types without resolving them. A value import would fail at load; a type
// import would resolve for nobody. Local structural types cost the compile-time link to Oh My Pi's
// definitions and buy a file that loads wherever Oh My Pi does.
type OmpEvent = Record<string, unknown> & { type: string }

interface OmpSessionManager {
  getSessionId?: () => string
  getCwd?: () => string
}

interface OmpContext {
  cwd?: string
  sessionManager?: OmpSessionManager
  model?: { id?: string; name?: string; provider?: string } | undefined
  mode?: string
  agent?: { kind?: string }
  getSystemPrompt?: () => string[]
}

// The events Beacon subscribes to, and nothing else.
//
// Oh My Pi publishes upwards of thirty event types. Most are provider-request internals, streaming
// message updates, compaction and retry signals, and TUI plumbing -- they describe how the runtime
// got its work done rather than what the agent did. Subscribing to all of them would fill the
// runtime log with rows no investigation asks for and would put Beacon in the path of every
// streaming token update.
//
// `context` is the one exception: its supported handler context exposes the effective system
// prompt. Beacon extracts only the `<skills>` index and the MCP tool routes, and discards the
// conversation messages and every other system-prompt section.
//
// `tool_call` and `tool_result` are the pair carrying tool activity: the first names the tool and
// its arguments before it runs, the second carries the outcome. `user_bash` and `user_python` are
// code the operator ran with Oh My Pi's `!` and `$` prefixes, which no tool event covers.
//
// `tool_approval_requested` and `tool_approval_resolved` are the events upstream Pi does not have,
// and they are the reason this extension is not simply the Pi one pointed at a different directory.
// They report a decision an operator was actually asked to make. Beacon refuses to synthesize an
// approval from a tool call -- a `tool_call` handler that blocks is an extension deciding, not an
// operator being asked -- so a runtime that reports real ones is a genuine capability gain.
//
// Deliberately absent: `mcp_notification`, which fires for every JSON-RPC notification a connected
// server sends, most of them routine tools/resources list refreshes. It describes MCP transport
// plumbing rather than an action the agent took. The MCP activity worth recording is the agent
// calling an MCP tool, which already arrives as a tool_call/tool_result pair.
//
// `message_end` carries each finalized assistant message, and on print and ACP -- which never emit
// `input` -- the operator's own prompt message as well (see submittedPromptText).
const subscribedEvents = [
  "session_start",
  "session_shutdown",
  "input",
  "context",
  "tool_call",
  "tool_result",
  "tool_approval_requested",
  "tool_approval_resolved",
  "user_bash",
  "user_python",
  "message_end",
] as const

// Oh My Pi renders its skill index under this heading in the runtime section of the system prompt,
// after a line of instructions. The match is anchored to the heading because the prompt also
// carries operator text -- project context files, appended prompts -- and a `<skills>` block quoted
// there is not the index the runtime gave the model.
//
// The index ends at the `</skills>` that is a line of its own. Oh My Pi writes each skill as one
// `- name: description` line, collapsing a description's newlines, so a `</skills>` a skill author
// puts in a description is always mid-line. Ending at the first `</skills>` anywhere would let one
// skill's description cut off its own remaining text and every skill listed after it.
const skillsSection = /^# Skills & Rules\n(?:[^\n]*\n){0,3}?<skills>\n([\s\S]*?)\n<\/skills>$/m

// Oh My Pi mounts each MCP tool as an `xd://` device and lists the devices in their own section of
// the system prompt: this heading, a line of instructions, then one row per tool. A row ends in the
// tool's description when its server gave one -- Oh My Pi's summary of it, which is the text the
// model reads before deciding to call the tool. Anchored to the heading for the same reason as
// skillsSection.
const mcpToolRoutesSection = /^## MCP Tool Routes\n(?:(?!- )[^\n]*\n){0,3}((?:- [^\n]*(?:\n|$))+)/gm
const mcpToolRouteRow = /^- "[^"]+" → `xd:\/\/(mcp__[^`]+)` — (.+)$/

// How many routes one listing carries. A session with more MCP tools than this has its first ones
// recorded rather than one payload per model call that grows with every server it connects.
const maxMCPToolRoutes = 256

interface MCPToolRoute {
  name: string
  description: string
}

// How many sessions' last-sent listings are remembered per kind, least recently sent evicted first.
const maxRememberedSessions = 64

// A listing the hook did not record is sent again on later model calls, up to this many attempts
// in all. The cap keeps a hook that can never record it -- no endpoint log configured, a hook binary
// older than this extension -- from costing a spawn before every model call.
const maxListingAttempts = 3

interface RememberedListing {
  listing: string
  attempts: number
  // Recorded, or given up on after maxListingAttempts.
  done: boolean
}

// The effective system prompt, read through Oh My Pi's supported accessor. An accessor that throws
// or is missing costs the listings, not the model call.
function systemPrompt(ctx: OmpContext | undefined): string[] {
  try {
    const prompt = ctx?.getSystemPrompt?.()
    if (Array.isArray(prompt)) return prompt.filter((block): block is string => typeof block === "string")
  } catch (err) {
    debugLog("system prompt could not be read", err)
  }
  return []
}

function skillListing(prompt: string[]): string {
  for (const block of prompt) {
    const listing = skillsSection.exec(block)?.[1]?.trim()
    if (listing) return listing
  }
  return ""
}

// The MCP tool routes the model was shown, each as the device name the model calls and the
// description its row carries. A route without a description is left out: it advertises nothing a
// rule could read.
//
// A section under the same heading that yields no routes -- operator text in a context file that
// borrows the heading for its own bullet list -- does not end the search, so it cannot hide the
// runtime's own section after it.
function mcpToolRoutes(prompt: string[]): MCPToolRoute[] {
  for (const block of prompt) {
    for (const [, rows = ""] of block.matchAll(mcpToolRoutesSection)) {
      const routes: MCPToolRoute[] = []
      for (const row of rows.split("\n")) {
        const [, name, description] = mcpToolRouteRow.exec(row) ?? []
        if (name && description?.trim()) routes.push({ name, description: description.trim() })
        if (routes.length === maxMCPToolRoutes) break
      }
      if (routes.length > 0) return routes
    }
  }
  return []
}

function rememberListing(seen: Map<string, RememberedListing>, key: string, listing: RememberedListing) {
  // Re-inserted rather than updated so the map's insertion order is send order, and eviction
  // drops the session that has gone longest without a change.
  seen.delete(key)
  if (seen.size >= maxRememberedSessions) {
    const oldest = seen.keys().next().value
    if (oldest !== undefined) seen.delete(oldest)
  }
  seen.set(key, listing)
}

// How many in-flight tool calls the approval enrichment below will remember at once.
//
// Bounded because the map is cleared by `tool_result`, and a call that never produces one -- an
// aborted run, a session torn down mid-tool -- would otherwise leave an entry behind for the life
// of the process. Oh My Pi runs a handful of tools concurrently at most, so this is far above the
// working set and small enough that the worst case is a few kilobytes.
const maxPendingToolCalls = 64

// Arguments of tool calls that have been proposed but not yet finished, keyed by the runtime's own
// tool call id.
//
// This exists for one reason: Oh My Pi's approval events name the tool and the call, but carry none
// of its arguments. An approval that says "the operator denied `bash`" is far less than one that
// says "the operator denied `rm -rf /`", and every approval detection Beacon ships matches on the
// command or file path rather than on the tool name -- so without this, approval telemetry is
// recorded but no detection can read it.
//
// The `tool_call` event carries those arguments and fires before the approval gate, in this same
// process, so remembering it until `tool_result` is exact: the join is the runtime's own call id,
// not a timestamp or a guess.
const pendingToolCalls = new Map<string, unknown>()
const rememberedSkillListings = new Map<string, RememberedListing>()
const rememberedMCPToolRoutes = new Map<string, RememberedListing>()

function rememberToolCall(event: OmpEvent) {
  const id = typeof event.toolCallId === "string" ? event.toolCallId : ""
  if (!id) return
  // Re-inserting moves the entry to the end of the Map's insertion order, which is what makes the
  // eviction below drop the genuinely oldest call rather than the least recently seen one.
  pendingToolCalls.delete(id)
  pendingToolCalls.set(id, event.input)
  while (pendingToolCalls.size > maxPendingToolCalls) {
    const oldest = pendingToolCalls.keys().next()
    if (oldest.done) break
    pendingToolCalls.delete(oldest.value)
  }
}

function forgetToolCall(event: OmpEvent) {
  const id = typeof event.toolCallId === "string" ? event.toolCallId : ""
  if (id) pendingToolCalls.delete(id)
}

// decidedToolInput returns the arguments of the tool call an approval event is deciding on.
//
// Absent rather than guessed when the call is unknown: an approval enriched with some other call's
// arguments would be worse than one with none, because it would read as evidence.
function decidedToolInput(event: OmpEvent): unknown {
  const id = typeof event.toolCallId === "string" ? event.toolCallId : ""
  if (!id) return undefined
  return pendingToolCalls.get(id)
}

// The text of a user message Oh My Pi delivered to the model, or "" when the message is not an
// operator's prompt.
//
// Runtime-generated turns (auto-continue, reminders) are marked `synthetic`, and messages one agent
// hands to another carry `attribution: "agent"`; neither is something an operator asked. Image
// parts are left out: the prompt row records what was asked, not the attachment bytes.
function userMessageText(message: unknown): string {
  if (!message || typeof message !== "object") return ""
  const { role, synthetic, attribution, content } = message as Record<string, unknown>
  if (role !== "user" || synthetic === true || attribution === "agent") return ""
  if (typeof content === "string") return content
  if (!Array.isArray(content)) return ""
  const texts: string[] = []
  for (const part of content as Array<{ type?: unknown; text?: unknown } | null>) {
    if (part?.type === "text" && typeof part.text === "string") texts.push(part.text)
  }
  return texts.join("\n")
}

function debugLog(message: string, extra?: unknown) {
  if (!debugEnabled) return
  try {
    // eslint-disable-next-line no-console
    console.error("[beacon-omp]", message, extra ?? "")
  } catch {
    // Debug logging must stay best-effort.
  }
}

// safeClone turns an Oh My Pi event into something JSON.stringify accepts.
//
// The events carry live objects: AbortSignals on the session events, AgentMessage arrays holding
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

// The most of the hook binary's reply that is read. The reply is one small object, {"recorded": n}.
const maxReplyBytes = 4096

// hookRecorded reads the hook binary's reply: whether it wrote at least one event. The exit status
// cannot say, because the hook exits 0 whether or not it wrote anything -- a hook that failed must
// not fail the run -- so a reply that is missing, unparseable or reports nothing means not recorded.
// Exported for tests.
export function hookRecorded(reply: string): boolean {
  try {
    const parsed: unknown = JSON.parse(reply)
    return typeof parsed === "object" && parsed !== null && "recorded" in parsed &&
      typeof parsed.recorded === "number" && parsed.recorded > 0
  } catch {
    return false
  }
}

// sendToBeacon resolves true only when the hook binary reports it recorded the event. Every failure
// is still swallowed -- telemetry never interrupts the run -- but a caller that sends something once
// rather than per event needs to know whether to try again.
async function sendToBeacon(payload: Record<string, unknown>): Promise<boolean> {
  // Flattened before anything else sees it, so every consumer of this function is handed plain
  // data rather than a live event with cycles and functions still attached.
  const safe = safeClone(payload)

  const testSender = (globalThis as Record<symbol, unknown>)[Symbol.for("beacon.omp.testSender")]
  if (typeof testSender === "function") {
    return (await (testSender as (value: unknown) => unknown)(safe)) !== false
  }

  let body: string
  try {
    body = JSON.stringify(safe)
  } catch (err) {
    debugLog("payload could not be serialized", err)
    return false
  }
  if (!body) return false

  try {
    // Imported lazily so a host without node:child_process fails to send rather than failing to
    // load: a throwing import at module scope would surface to the user as a broken extension, and
    // Oh My Pi reports an extension that throws at load time rather than continuing without it.
    const { spawn } = await import("node:child_process")
    return await new Promise<boolean>((resolve) => {
      let settled = false
      const finish = (ok: boolean) => {
        if (settled) return
        settled = true
        resolve(ok)
      }
      let child
      try {
        child = spawn(beaconArgv[0], beaconArgv.slice(1), {
          stdio: ["pipe", "pipe", "ignore"],
          windowsHide: true,
        })
      } catch (err) {
        debugLog("hook binary could not be spawned", err)
        finish(false)
        return
      }
      const timer = setTimeout(() => {
        try {
          child.kill()
        } catch {
          // Already gone.
        }
        debugLog("hook binary timed out", { type: payload?.type })
        finish(false)
      }, sendTimeoutMs)
      // An unreferenced timer cannot keep Oh My Pi alive at exit waiting on Beacon telemetry.
      if (typeof timer.unref === "function") timer.unref()
      const done = (ok: boolean) => {
        clearTimeout(timer)
        finish(ok)
      }
      child.on("error", (err) => {
        debugLog("hook binary failed", err)
        done(false)
      })
      let reply = ""
      child.stdout?.on("data", (chunk: Buffer) => {
        if (reply.length < maxReplyBytes) reply += chunk.toString("utf8")
      })
      child.stdout?.on("error", () => {})
      // `close` fires after the child's stdio has closed, so the whole reply has arrived.
      child.on("close", (code: number | null) => done(code === 0 && hookRecorded(reply)))
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
    // Beacon telemetry must never interrupt Oh My Pi execution.
    return false
  }
}

// sendListing sends a listing read from the system prompt the first time a session shows it and
// again whenever it changes. One the hook did not record is sent again on the next model call, so a
// failed send does not lose it for the session. `listing` is what a session is compared on, and
// `payload` is what leaves the extension.
async function sendListing(
  seen: Map<string, RememberedListing>,
  key: string,
  listing: string,
  payload: Record<string, unknown>,
) {
  const remembered = seen.get(key)
  const same = remembered?.listing === listing
  if (!listing || (same && remembered?.done)) return
  const attempts = same && remembered ? remembered.attempts + 1 : 1
  const recorded = await sendToBeacon(payload)
  if (!recorded && attempts >= maxListingAttempts) {
    debugLog("listing was not recorded; not sending it again", { attempts })
  }
  rememberListing(seen, key, { listing, attempts, done: recorded || attempts >= maxListingAttempts })
}

// identity lifts the session and workspace fields Beacon needs to the top level of the envelope.
//
// Oh My Pi keeps them on the handler context rather than on the event, and behind accessor
// functions rather than as properties, so they have to be read per event: a session id captured
// once at extension load would be wrong after `/new`, a resume, a fork, or a tree navigation, all
// of which replace the session without reloading the extension.
//
// Each accessor is called defensively. `getSessionId` and `getCwd` are documented members of the
// read-only session manager, but an extension that loses its session id loses the field that groups
// every event of a run, so a throwing accessor costs that field rather than the event.
function identity(ctx: OmpContext | undefined): Record<string, unknown> {
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
    // Oh My Pi's Model carries an id and a provider separately. Joined here so the envelope's
    // `model` is one string, matching how every other Beacon collection path reports it.
    const name = model.id || model.name
    if (name) fields.model = model.provider ? `${model.provider}/${name}` : name
  }
  // Which surface the session is running on: tui, rpc, json or print. A `print` or `rpc` run is
  // unattended, which changes how an approval row should be read -- there was no human at the
  // terminal to ask.
  if (ctx.mode) fields.ompMode = ctx.mode
  return fields
}

type OmpExtensionAPI = {
  on: (event: string, handler: (event: OmpEvent, ctx: OmpContext) => Promise<void> | void) => void
}

// createBeaconExtension is exported for tests; Oh My Pi loads the default export below.
export function createBeaconExtension() {
  // The id of the current session once it has emitted `input` ("" for a session without one), or
  // null until it has. Interactive and RPC sessions emit `input` for every submission; ACP and
  // print never do. RPC and ACP both report `ctx.mode` "rpc", so this is what tells them apart.
  let inputSessionId: string | null = null

  // submittedPromptText decides whether a user message is a prompt no `input` event recorded.
  //
  // Interactive sessions are excluded outright: every prompt typed there arrives as `input`, and a
  // user message without one was sent by an extension, not typed. A session that has emitted
  // `input` is an RPC client, which reports every submission the same way. A subagent's messages
  // come from its parent's task, not an operator.
  const submittedPromptText = (message: unknown, ctx: OmpContext | undefined, sessionId: string) => {
    if (ctx?.mode === "tui" || ctx?.agent?.kind === "sub" || inputSessionId === sessionId) return ""
    return userMessageText(message)
  }

  const forward = async (event: OmpEvent, ctx: OmpContext) => {
    try {
      const fields = identity(ctx)
      if (event.type === "context") {
        // Only the listings leave the extension, and only when one changed: `context` fires before
        // every model call and carries the whole conversation. The skill index and the MCP routes
        // are sent and remembered apart, so a change to one does not resend the other.
        //
        // A session without an id is remembered under "", so it is still sent once rather than
        // before every model call.
        const key = typeof fields.sessionId === "string" ? fields.sessionId : ""
        const prompt = systemPrompt(ctx)
        const listing = skillListing(prompt)
        await sendListing(rememberedSkillListings, key, listing, { ...fields, type: "context", skillListing: listing })
        const routes = mcpToolRoutes(prompt)
        await sendListing(rememberedMCPToolRoutes, key, routes.length > 0 ? JSON.stringify(routes) : "", {
          ...fields,
          type: "context",
          mcpToolRoutes: routes,
        })
        return
      }
      // The event is spread last so a field it carries itself wins over the context's. `user_bash`
      // and `user_python` both carry their own cwd, and the approval events carry their own
      // sessionId -- in each case the event's is the one that describes this action.
      const envelope: Record<string, unknown> = { ...fields, ...event }
      const sessionId = typeof envelope.sessionId === "string" ? envelope.sessionId : ""

      if (event.type === "input") {
        inputSessionId = sessionId
      } else if (event.type === "message_end" && (event.message as { role?: unknown })?.role === "user") {
        // Only the role and text leave the extension: a user message can also carry image bytes
        // and runtime bookkeeping the prompt row has no use for. One that is not a prompt is not
        // sent at all; the mapper records nothing for it.
        const prompt = submittedPromptText(event.message, ctx, sessionId)
        if (!prompt) return
        await sendToBeacon({ ...fields, type: "message_end", message: { role: "user" }, prompt })
        return
      }

      if (event.type === "tool_call") {
        rememberToolCall(event)
      } else if (event.type === "tool_result") {
        forgetToolCall(event)
      } else if (event.type === "session_start" || event.type === "session_shutdown") {
        // `/new`, a resume, a fork and a shutdown all end the run these calls belonged to. Clearing
        // keeps a call abandoned mid-flight from being joined to an approval in a later session.
        // Done here rather than in a second `pi.on` registration so the whole cache lifecycle is
        // visible in one place.
        pendingToolCalls.clear()
        inputSessionId = null
        rememberedSkillListings.delete(sessionId)
        rememberedMCPToolRoutes.delete(sessionId)
      } else if (event.type === "tool_approval_requested" || event.type === "tool_approval_resolved") {
        // Attached under `input`, the same key `tool_call` uses, so the mapper reads one shape for
        // both and an approval resolves to the same command or file path its tool call did.
        const decided = decidedToolInput(event)
        if (decided !== undefined) envelope.input = decided
      }

      await sendToBeacon(envelope)
    } catch (err) {
      // Unreachable in practice: sendToBeacon swallows its own failures. Kept because a handler
      // that rejects is a handler that can break the run it was observing.
      debugLog("handler failed", err)
    }
  }

  return {
    register(pi: OmpExtensionAPI) {
      for (const name of subscribedEvents) {
        // Every handler returns undefined. Beacon observes; it never blocks a tool call, revises a
        // tool's arguments, rewrites a prompt, or replaces a message. Oh My Pi reads a returned
        // object as a request to change behavior -- `{ block: true }` or `{ input }` on tool_call,
        // `{ result }` on user_bash and user_python, `{ content }` on tool_result -- so returning
        // nothing is what keeps this extension an observer. Enforcement lives behind the policy
        // provider seam in the hook adapter, not here.
        pi.on(name, forward)
      }
    },
    // Exposed so a test can assert the subscription list without a live Oh My Pi instance.
    subscribedEvents: [...subscribedEvents],
  }
}

export default function beaconExtension(pi: OmpExtensionAPI) {
  createBeaconExtension().register(pi)
}
