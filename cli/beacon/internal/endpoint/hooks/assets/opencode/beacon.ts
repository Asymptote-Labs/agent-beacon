// __BEACON_MANAGED_MARKER__
// Beacon endpoint telemetry plugin for opencode.
// Managed by beacon endpoint hooks install --harness opencode.

const beaconCommand = "__BEACON_COMMAND__"
const debugEnabled = process.env.BEACON_OPENCODE_DEBUG === "1"
const sendTimeoutMs = 2000
const directHookTypes = new Set([
  "chat.message",
  "command.execute.before",
  "tool.execute.before",
  "tool.execute.after",
])
const forwardedEvents = new Set([
  "file.edited",
  "file.watcher.updated",
  "message.part.updated",
  "message.part.delta",
  "message.updated",
  "permission.v2.asked",
  "permission.v2.replied",
  "session.created",
  "session.idle",
  "session.status",
  "session.error",
  "session.diff",
  "command.executed",
  "permission.asked",
  "permission.replied",
  "permission.updated",
])

async function debugLog(client, message, extra) {
  if (!debugEnabled) return
  try {
    await client?.app?.log?.({
      body: {
        service: "beacon-opencode-plugin",
        level: "debug",
        message,
        extra,
      },
    })
  } catch {
    // Debug logging must stay best-effort.
  }
}

async function sendToBeacon(client, payload) {
  let proc
  try {
    proc = Bun.spawn(["/bin/sh", "-lc", beaconCommand], {
      stdin: "pipe",
      stdout: "ignore",
      stderr: "ignore",
    })
    proc.stdin.write(JSON.stringify(payload))
    proc.stdin.end()
    const outcome = await Promise.race([
      proc.exited.then((code) => ({ code })),
      Bun.sleep(sendTimeoutMs).then(() => ({ timeout: true })),
    ])
    if ("timeout" in outcome) {
      proc.kill()
      await debugLog(client, "Beacon hook command timed out", { type: payload?.type })
      return
    }
    const code = outcome.code
    if (code !== 0) {
      await debugLog(client, "Beacon hook command exited non-zero", { code, type: payload?.type })
    }
  } catch (err) {
    await debugLog(client, "Beacon hook command failed", {
      error: err instanceof Error ? err.message : String(err),
      type: payload?.type,
    })
    // Beacon telemetry must never interrupt opencode execution.
  }
}

function sessionID(value) {
  const direct = value?.sessionID || value?.session_id || value?.session?.id || value?.info?.sessionID
  if (direct) return direct
  if (typeof value?.id === "string" && value.id.startsWith("ses_")) return value.id
  if (typeof value?.info?.id === "string" && value.info.id.startsWith("ses_")) return value.info.id
  return ""
}

function modelName(value) {
  const model = value?.model || value?.modelInfo
  if (!model || typeof model === "string") return model || ""
  if (model.providerID && model.modelID) return model.providerID + "/" + model.modelID
  return model.modelID || model.id || model.name || ""
}

function filePath(value) {
  return (
    value?.filePath ||
    value?.file_path ||
    value?.path ||
    value?.file ||
    value?.target ||
    value?.destination ||
    ""
  )
}

function isFileMutationTool(tool) {
  const name = String(tool || "").toLowerCase()
  return ["edit", "write", "patch", "apply_patch", "create"].some((part) => name.includes(part))
}

export const BeaconEndpointPlugin = async ({ project, directory, worktree, client }) => {
  const context = { project, directory, worktree }
  const activeCalls = new Map()
  const completedCalls = new Set()
  const emittedParts = new Set()
  const pendingParts = new Map()
  const partDeltas = new Map()
  const messageModels = new Map()
  const recentFilePaths = new Map()
  let eventQueue = Promise.resolve()

  const payload = (type, values = {}) => ({ type, ...values, ...context })
  const enqueue = (value) => {
    eventQueue = eventQueue
      .then(() => sendToBeacon(client, value))
      .catch((err) => debugLog(client, "Beacon event queue failed", { error: String(err), type: value?.type }))
    return eventQueue
  }
  const rememberFilePath = (tool, args) => {
    if (!isFileMutationTool(tool)) return
    const path = filePath(args)
    if (path) recentFilePaths.set(path, Date.now())
  }
  const partKey = (part) => `${part?.sessionID || ""}:${part?.messageID || ""}:${part?.id || ""}`
  const emitPart = (part, sid, model) => {
    if (!part) return
    const key = partKey(part)
    if (emittedParts.has(key)) return
    const next = structuredClone(part)
    const delta = partDeltas.get(key)
    if ((next.type === "text" || next.type === "reasoning") && !next.text && delta) next.text = delta
    if (next.type === "tool") {
      const status = next.state?.status
      if (status !== "completed" && status !== "error") return
      if (status === "completed" && completedCalls.has(next.callID)) return
    } else if (next.type === "text" || next.type === "reasoning") {
      if (!next.time?.end) {
        pendingParts.set(key, { part: next, session_id: sid, model })
        return
      }
    } else if (next.type !== "retry") {
      return
    }
    emittedParts.add(key)
    pendingParts.delete(key)
    partDeltas.delete(key)
    void enqueue(payload("message.part.updated", { session_id: sid, model, part: next }))
  }
  const flushParts = (sid) => {
    for (const [key, item] of pendingParts) {
      if (item.session_id !== sid) continue
      const part = { ...item.part, time: { ...(item.part.time || {}), end: Date.now() } }
      pendingParts.delete(key)
      emitPart(part, sid, item.model)
    }
  }

  return {
    "chat.message": async (input, output) => {
      await sendToBeacon(client, {
        type: "chat.message",
        session_id: sessionID(input),
        model: modelName(input),
        input,
        output,
        ...context,
      })
    },
    "command.execute.before": async (input, output) => {
      await sendToBeacon(
        client,
        payload("command.execute.before", {
          session_id: sessionID(input),
          command_name: input?.command,
          arguments: input?.arguments,
          parts: output?.parts,
        }),
      )
    },
    "tool.execute.before": async (input, output) => {
      activeCalls.set(input?.callID, {
        tool: input?.tool,
        args: structuredClone(output?.args || {}),
        started_at: Date.now(),
      })
      await sendToBeacon(
        client,
        payload("tool.execute.before", {
          session_id: sessionID(input),
          tool_name: input?.tool,
          call_id: input?.callID,
          tool_input: output?.args || {},
        }),
      )
    },
    "tool.execute.after": async (input, output) => {
      const active = activeCalls.get(input?.callID)
      const args = input?.args || active?.args || {}
      rememberFilePath(input?.tool, args)
      await sendToBeacon(
        client,
        payload("tool.execute.after", {
          session_id: sessionID(input),
          tool_name: input?.tool,
          call_id: input?.callID,
          duration_ms: active?.started_at ? Date.now() - active.started_at : undefined,
          tool_input: args,
          tool_response: output,
        }),
      )
      activeCalls.delete(input?.callID)
      completedCalls.add(input?.callID)
    },
    event: async ({ event }) => {
      const type = event?.type || "event"
      if (!forwardedEvents.has(type)) return

      const properties = event?.properties || event?.data || {}
      const sid = sessionID(properties)
      const info = properties?.info || {}
      if (type === "message.updated") {
        if (info?.role !== "assistant") return
        const model = modelName(info)
        if (info?.id && model) messageModels.set(info.id, model)
        if (!info?.time?.completed && !info?.error) return
      }
      if (type === "message.part.delta") {
        const key = `${sid}:${properties?.messageID || ""}:${properties?.partID || ""}`
        partDeltas.set(key, (partDeltas.get(key) || "") + (properties?.delta || ""))
        return
      }
      if (type === "message.part.updated") {
        const part = properties?.part
        emitPart(part, sid, messageModels.get(part?.messageID) || "")
        return
      }
      if (type === "session.status" && properties?.status?.type === "idle") flushParts(sid)
      if (type === "session.idle") flushParts(sid)
      if (type === "session.diff") {
        const diffs = Array.isArray(properties?.diff) ? properties.diff : []
        const now = Date.now()
        const filtered = diffs.filter((item) => {
          const path = filePath(item)
          if (!path) return false
          const seen = recentFilePaths.get(path)
          if (seen && now - seen < 5000) return false
          return item?.before !== item?.after || item?.additions || item?.deletions
        })
        if (filtered.length === 0) return
        properties.diff = filtered
      }
      if (type === "file.edited" || type === "file.watcher.updated") {
        if (!sid) return
      }
      void enqueue(
        payload(type, {
          session_id: sid,
          model: modelName(info || properties),
          properties,
          event,
        }),
      )
    },
  }
}
