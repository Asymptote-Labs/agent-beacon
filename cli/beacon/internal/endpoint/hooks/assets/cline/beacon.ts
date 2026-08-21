// __BEACON_MANAGED_MARKER__
// Beacon endpoint telemetry plugin for Cline.
// Managed by beacon endpoint hooks install --harness cline.

// The argv Beacon's installer writes, as an array rather than a command line.
//
// Cline plugins run inside whichever host loaded them -- the VS Code extension host, a JetBrains
// plugin host, or the CLI -- so the hook may be spawned on Windows as readily as on macOS. An argv
// array is passed straight to the OS with no shell in between, which sidesteps quoting entirely: a
// POSIX-quoted command line is not valid in cmd.exe, and a Windows path inside double quotes is
// still expanded by bash.
const beaconArgv: string[] = ["__BEACON_ARGV__"]

const debugEnabled = process.env.BEACON_CLINE_DEBUG === "1"

// A send that has not finished in this long is abandoned. Cline awaits these handlers, so this is
// the worst case Beacon can add to a tool call.
const sendTimeoutMs = 2000

// How deep into a hook context the serializer will walk before giving up.
const maxDepth = 8

function debugLog(message: string, extra?: unknown) {
  if (!debugEnabled) return
  try {
    // eslint-disable-next-line no-console
    console.error("[beacon-cline]", message, extra ?? "")
  } catch {
    // Debug logging must stay best-effort.
  }
}

// safeClone turns a hook context into something JSON.stringify accepts.
//
// The contexts Cline passes are live SDK objects, not plain data: they carry functions, class
// instances, and parent references. JSON.stringify throws on a circular reference and on a bigint,
// and silently drops an Error to {}, so each of those is handled here rather than losing the whole
// payload to one bad field.
//
// Cycles are tracked along the current path and released on the way out, so an object that legibly
// appears twice -- the same workspace root referenced from two places -- is kept both times. A
// WeakSet that never released would drop the second occurrence as if it were a cycle.
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
  // data. Doing it further down, on the way into the child process, would leave a live SDK object
  // -- cycles, functions and all -- reaching whatever else is wired up here.
  const safe = safeClone(payload)

  const testSender = (globalThis as Record<symbol, unknown>)[Symbol.for("beacon.cline.testSender")]
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
    // load: a throwing import at module scope would surface to the user as a broken plugin.
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
      // An unreferenced timer cannot keep a CLI host alive waiting on Beacon telemetry.
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
    // Beacon telemetry must never interrupt Cline execution.
  }
}

// Fields Beacon needs at the top level of the envelope, lifted out of wherever the context keeps
// them.
//
// Best-effort by design: Cline documents the base fields every hook receives, but not where each
// handler's context puts them, so several shapes are tried and a miss costs the field rather than
// the event. The hook adapter reads the same names, so anything missed here is still recoverable
// from the forwarded context.
function identity(context: Record<string, unknown> | undefined): Record<string, unknown> {
  if (!context) return {}
  const task = (context.task ?? context.run ?? {}) as Record<string, unknown>
  const fields: Record<string, unknown> = {}
  const taskId = context.taskId ?? context.taskID ?? context.task_id ?? task.id ?? task.taskId
  if (taskId) fields.taskId = taskId
  const roots = context.workspaceRoots ?? context.workspace_roots ?? context.workspaceFolders
  if (roots) fields.workspaceRoots = roots
  const cwd = context.cwd ?? context.workingDirectory ?? context.working_directory
  if (cwd) fields.cwd = cwd
  const version = context.clineVersion ?? context.cline_version ?? context.version
  if (version) fields.clineVersion = version
  return fields
}

type HookContext = Record<string, unknown> | undefined

export function createBeaconPlugin() {
  let base: Record<string, unknown> = {}

  const forward = async (type: string, context: HookContext) => {
    try {
      await sendToBeacon({ ...base, ...identity(context), ...(context ?? {}), type })
    } catch (err) {
      // Unreachable in practice: sendToBeacon swallows its own failures. Kept because a hook that
      // rejects is a hook that can break the run it was observing.
      debugLog("hook failed", err)
    }
  }

  return {
    name: "beacon-endpoint",
    manifest: { capabilities: ["hooks"] },

    // The setup context carries the workspace identity for every later hook, so it is captured
    // once rather than re-derived per event.
    setup(_api: unknown, context: HookContext) {
      base = identity(context)
    },

    hooks: {
      // Every handler returns undefined. Beacon observes; it never cancels a tool call, modifies a
      // prompt, or rewrites a tool result. Enforcement lives behind the policy provider seam in the
      // hook adapter, not here.
      beforeRun: (context: HookContext) => forward("beforeRun", context),
      beforeTool: (context: HookContext) => forward("beforeTool", context),
      afterTool: (context: HookContext) => forward("afterTool", context),
      afterRun: (context: HookContext) => forward("afterRun", context),
    },
  }
}

export const BeaconEndpointPlugin = createBeaconPlugin()

export default BeaconEndpointPlugin
