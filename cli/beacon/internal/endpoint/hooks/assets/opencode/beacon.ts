// __BEACON_MANAGED_MARKER__
// Beacon endpoint telemetry plugin for opencode.
// Managed by beacon endpoint hooks install --harness opencode.

const beaconCommand = "__BEACON_COMMAND__"
const pluginID = "beacon.endpoint"
const debugEnabled = process.env.BEACON_OPENCODE_DEBUG === "1"
const sendTimeoutMs = 2000
const directHookTypes = new Set([
  "chat.message",
  "command.execute.before",
  "tool.execute.before",
  "tool.execute.after",
])
const forwardedEvents = new Set([
  "message.part.updated",
  "message.part.delta",
  "message.updated",
  "permission.v2.asked",
  "permission.v2.replied",
  "session.created",
  "session.deleted",
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
  const testSender = globalThis[Symbol.for("beacon.opencode.testSender")]
  if (typeof testSender === "function") {
    await testSender(payload)
    return
  }
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
      await Promise.race([proc.exited.catch(() => undefined), Bun.sleep(250)])
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
  if (value?.providerID && value?.modelID) return value.providerID + "/" + value.modelID
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
  const tokens = name.split(/[^a-z0-9]+/).filter(Boolean)
  return (
    ["edit", "write", "patch", "create"].some((part) => tokens.includes(part)) ||
    ["applypatch", "multiedit"].includes(name)
  )
}

function shellMutationPaths(tool, args) {
  const name = String(tool || "").toLowerCase()
  if (name !== "bash" && !name.includes("shell")) return []
  const command = String(args?.command || args?.cmd || "")
  const paths = []
  const pattern =
    /(?:(?:^|&&|\|\||;|\n)[ \t]*)(?:sudo\s+)?(?:command\s+)?(?:\/bin\/)?(?:rm|unlink)\s+(?:-[^\s]+\s+)*(?:"([^"]+)"|'([^']+)'|([^\s;&|]+))/g
  for (const match of command.matchAll(pattern)) {
    const path = match[1] || match[2] || match[3]
    if (path) paths.push(path)
  }
  return paths
}

export const BeaconEndpointPlugin = async ({ project, directory, worktree, client }) => {
  const context = { project, directory, worktree }
  const activeCalls = new Map()
  const completedCalls = new Map()
  const emittedParts = new Set()
  const pendingParts = new Map()
  const partDeltas = new Map()
  const messageModels = new Map()
  const messageRoles = new Map()
  const messageSessions = new Map()
  const completedMessages = new Set()
  const permissionRequests = new Map()
  const sessionStates = new Map()
  const recentFilePaths = new Map()
  let eventQueue = Promise.resolve()

  const payload = (type, values = {}) => ({ type, ...values, ...context })
  const enqueue = (value) => {
    eventQueue = eventQueue
      .then(() => sendToBeacon(client, value))
      .catch((err) => debugLog(client, "Beacon event queue failed", { error: String(err), type: value?.type }))
    return eventQueue
  }
  const rememberFilePath = (tool, args, sid, callID, includeShell = false) => {
    const paths = []
    if (isFileMutationTool(tool) && filePath(args)) paths.push(filePath(args))
    if (includeShell) paths.push(...shellMutationPaths(tool, args))
    for (const path of paths) recentFilePaths.set(path, { time: Date.now(), sessionID: sid, callID })
  }
  const partKey = (part, fallbackSessionID) =>
    `${part?.sessionID || fallbackSessionID || ""}:${part?.messageID || ""}:${part?.id || ""}`
  const emitPart = (part, sid, model) => {
    if (!part) return
    const key = partKey(part, sid)
    if (emittedParts.has(key)) return
    const next = structuredClone(part)
    const delta = partDeltas.get(key)
    if ((next.type === "text" || next.type === "reasoning") && !next.text && delta) next.text = delta
    if (next.type === "tool") {
      const status = next.state?.status
      if (status !== "completed" && status !== "error") return
      if (status === "completed" && completedCalls.has(next.callID)) return
    } else if (next.type === "text" || next.type === "reasoning") {
      if (next.type === "text") {
        const role = messageRoles.get(next.messageID)
        if (role === "user") {
          emittedParts.add(key)
          pendingParts.delete(key)
          partDeltas.delete(key)
          return
        }
        if (role !== "assistant") {
          pendingParts.set(key, { part: next, session_id: sid, model })
          return
        }
      }
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
    for (const [key, item] of [...pendingParts]) {
      if (item.session_id !== sid) continue
      const part = { ...item.part, time: { ...(item.part.time || {}), end: Date.now() } }
      pendingParts.delete(key)
      emitPart(part, sid, item.model)
    }
  }
  const flushMessageParts = (messageID, sid, model, completedAt) => {
    for (const [key, item] of [...pendingParts]) {
      if (item.part?.messageID !== messageID) continue
      const part = { ...item.part, time: { ...(item.part.time || {}), end: completedAt || Date.now() } }
      pendingParts.delete(key)
      emitPart(part, sid, model || item.model)
    }
  }
  const cleanupSession = (sid) => {
    for (const [callID, item] of activeCalls) if (item.session_id === sid) activeCalls.delete(callID)
    for (const [callID, session] of completedCalls) if (session === sid) completedCalls.delete(callID)
    for (const key of emittedParts) if (key.startsWith(`${sid}:`)) emittedParts.delete(key)
    for (const [key, item] of pendingParts) if (item.session_id === sid) pendingParts.delete(key)
    for (const key of partDeltas.keys()) if (key.startsWith(`${sid}:`)) partDeltas.delete(key)
    for (const [messageID, session] of messageSessions) {
      if (session !== sid) continue
      messageSessions.delete(messageID)
      messageModels.delete(messageID)
      messageRoles.delete(messageID)
      completedMessages.delete(messageID)
    }
    for (const [requestID, request] of permissionRequests) {
      if (request.sessionID === sid) permissionRequests.delete(requestID)
    }
    sessionStates.delete(sid)
    for (const [path, item] of recentFilePaths) if (item.sessionID === sid) recentFilePaths.delete(path)
  }

  return {
    "chat.message": async (input, output) => {
      if (output?.message?.id) {
        messageRoles.set(output.message.id, "user")
        messageSessions.set(output.message.id, sessionID(input))
      }
      await enqueue({
        type: "chat.message",
        session_id: sessionID(input),
        model: modelName(input),
        input,
        output,
        ...context,
      })
    },
    "command.execute.before": async (input, output) => {
      await enqueue(
        payload("command.execute.before", {
          session_id: sessionID(input),
          command_name: input?.command,
          arguments: input?.arguments,
          parts: output?.parts,
        }),
      )
    },
    "tool.execute.before": async (input, output) => {
      const sid = sessionID(input)
      activeCalls.set(input?.callID, {
        tool: input?.tool,
        args: structuredClone(output?.args || {}),
        session_id: sid,
        started_at: Date.now(),
      })
      rememberFilePath(input?.tool, output?.args || {}, sid, input?.callID)
      await enqueue(
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
      const exitCode = output?.metadata?.exit ?? output?.metadata?.exitCode ?? output?.metadata?.exit_code
      rememberFilePath(input?.tool, args, sessionID(input), input?.callID, exitCode === 0)
      await enqueue(
        payload("tool.execute.after", {
          session_id: sessionID(input),
          tool_name: input?.tool,
          call_id: input?.callID,
          duration_ms: active?.started_at ? Date.now() - active.started_at : undefined,
          tool_input: args,
          tool_response: output,
          file_mutations:
            exitCode === 0
              ? shellMutationPaths(input?.tool, args).map((path) => ({ path, operation: "delete" }))
              : [],
        }),
      )
      activeCalls.delete(input?.callID)
      completedCalls.set(input?.callID, sessionID(input))
    },
    event: async ({ event }) => {
      const type = event?.type || "event"
      if (!forwardedEvents.has(type)) return

      const properties = event?.properties || event?.data || {}
      const sid = sessionID(properties)
      const info = properties?.info || {}
      if (type === "message.updated") {
        if (info?.id) {
          if (info?.role) messageRoles.set(info.id, info.role)
          messageSessions.set(info.id, sid)
        }
        if (info?.role !== "assistant") return
        const model = modelName(info)
        if (info?.id && model) messageModels.set(info.id, model)
        if (!info?.time?.completed && !info?.error) return
        flushMessageParts(info.id, sid, model, info?.time?.completed)
        if (info?.id) {
          if (completedMessages.has(info.id)) return
          if (!info.finish || info.finish === "stop") completedMessages.add(info.id)
        }
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
      if (type === "session.status") {
        const status = properties?.status?.type || ""
        if (sessionStates.get(sid) === status) return
        sessionStates.set(sid, status)
        if (status === "idle") flushParts(sid)
      }
      if (type === "session.idle") {
        flushParts(sid)
        if (sessionStates.get(sid) === "idle") return
        sessionStates.set(sid, "idle")
      }
      if (type === "session.deleted") flushParts(sid)
      if (type === "permission.asked" || type === "permission.v2.asked") {
        if (properties?.id) {
          permissionRequests.set(properties.id, {
            tool: properties?.tool,
            permission: properties?.permission,
            sessionID: sid,
          })
        }
      }
      if (type === "permission.replied" || type === "permission.v2.replied") {
        const request = permissionRequests.get(properties?.requestID)
        if (request?.tool && !properties.tool) properties.tool = request.tool
        if (request?.permission && !properties.permission) properties.permission = request.permission
        permissionRequests.delete(properties?.requestID)
      }
      if (type === "session.diff") {
        const diffs = Array.isArray(properties?.diff) ? properties.diff : []
        const now = Date.now()
        const filtered = diffs.filter((item) => {
          const path = filePath(item)
          if (!path) return false
          const seen = recentFilePaths.get(path)
          if (seen && now - seen.time < 5000) return false
          return item?.before !== item?.after || item?.additions || item?.deletions
        })
        if (filtered.length === 0) return
        properties.diff = filtered
      }
      const queued = enqueue(
        payload(type, {
          session_id: sessionID(properties) || sid,
          model: modelName(Object.keys(info).length > 0 ? info : properties),
          properties,
          event,
        }),
      )
      if (type === "session.deleted") void queued.finally(() => cleanupSession(sid))
    },
  }
}

// OpenCode 2.x loads only a default definition with an id and a setup
// function; 1.x reads the same default export's server field. setup adapts
// the V2 hooks and event stream onto the V1 handlers above, so both runtimes
// send the same payloads to beacon-hooks.
function v2Model(ref) {
  if (!ref?.id) return undefined
  return ref.providerID ? { providerID: ref.providerID, modelID: ref.id } : { modelID: ref.id }
}

function v2PromptParts(prompt) {
  const parts = []
  if (prompt?.text) parts.push({ type: "text", text: prompt.text })
  for (const file of prompt?.files || []) {
    if (file?.uri) parts.push({ type: "file", filename: file.name || file.uri, url: file.uri })
  }
  for (const agent of prompt?.agents || []) {
    if (agent?.name) parts.push({ type: "agent", name: agent.name })
  }
  return parts
}

function v2ToolResponse(result) {
  const structured = result?.output
  const text = (result?.content || [])
    .filter((item) => item?.type === "text" && typeof item.text === "string")
    .map((item) => item.text)
    .join("\n")
  const output = typeof structured === "string" ? structured : structured?.output ?? text
  const metadata = { ...(result?.metadata || {}) }
  if (metadata.exit === undefined && typeof structured?.exit === "number") metadata.exit = structured.exit
  return { output, metadata }
}

// Translates one V2 event into the V1 {type, properties} events the handler
// understands. Tool success and failure arrive through the tool hooks instead.
function v2Events(event, models) {
  const data = event?.data || {}
  const sid = data.sessionID
  const created = typeof event?.created === "number" ? event.created : Date.now()
  switch (event?.type) {
    case "session.created":
    case "session.model.selected": {
      const model = v2Model(data.model)
      if (sid && model) models.set(sid, model)
      if (event.type !== "session.created") return []
      return [{ type: "session.created", properties: { ...data, info: { ...data, id: sid, ...(model || {}) } } }]
    }
    case "session.deleted":
    case "session.idle":
    case "session.status":
    case "permission.replied":
      return [{ type: event.type, properties: data }]
    case "permission.asked":
      return [
        {
          type: "permission.asked",
          properties: { ...data, permission: data.action, patterns: data.resources },
        },
      ]
    case "session.execution.started":
      return [{ type: "session.status", properties: { sessionID: sid, status: { type: "busy" } } }]
    case "session.execution.succeeded":
    case "session.execution.failed":
    case "session.execution.interrupted": {
      const events = []
      if (event.type === "session.execution.failed") {
        events.push({ type: "session.error", properties: { sessionID: sid, error: data.error } })
      }
      events.push({ type: "session.status", properties: { sessionID: sid, status: { type: "idle" } } })
      return events
    }
    case "session.text.ended":
    case "session.reasoning.ended": {
      const messageID = data.assistantMessageID
      const type = event.type === "session.text.ended" ? "text" : "reasoning"
      return [
        {
          type: "message.updated",
          properties: { sessionID: sid, info: { id: messageID, role: "assistant", sessionID: sid, ...(models.get(sid) || {}) } },
        },
        {
          type: "message.part.updated",
          properties: {
            sessionID: sid,
            part: {
              id: `${messageID}:${type}:${data.ordinal ?? 0}`,
              messageID,
              sessionID: sid,
              type,
              text: data.text || "",
              time: { start: created, end: created },
            },
          },
        },
      ]
    }
    case "session.step.ended":
      return [
        {
          type: "message.updated",
          properties: {
            sessionID: sid,
            info: {
              id: data.assistantMessageID,
              role: "assistant",
              sessionID: sid,
              ...(models.get(sid) || {}),
              finish: data.finish,
              tokens: data.tokens,
              cost: data.cost,
              time: { completed: created },
            },
          },
        },
      ]
    default:
      return []
  }
}

export default {
  id: pluginID,
  server: BeaconEndpointPlugin,
  async setup(ctx) {
    const location = ctx?.location || {}
    const hooks = await BeaconEndpointPlugin({
      project: location.project,
      directory: location.directory,
      worktree: location.project?.canonical || location.project?.directory || location.directory,
      client: ctx,
    })
    const models = new Map()
    const registrations = []
    const register = async (domain, name, callback) => {
      try {
        const registration = await domain?.hook?.(name, async (input) => {
          try {
            await callback(input)
          } catch (err) {
            await debugLog(ctx, "Beacon V2 hook failed", { name, error: String(err) })
          }
        })
        if (registration) registrations.push(registration)
      } catch (err) {
        await debugLog(ctx, "Beacon V2 hook registration failed", { name, error: String(err) })
      }
    }

    await register(ctx?.session, "prompt", (input) =>
      hooks["chat.message"](
        { sessionID: input?.sessionID, messageID: input?.messageID, ...(models.get(input?.sessionID) || {}) },
        { message: { id: input?.messageID }, parts: v2PromptParts(input?.prompt) },
      ),
    )
    await register(ctx?.tool, "execute.before", (input) =>
      hooks["tool.execute.before"](
        { sessionID: input?.sessionID, tool: input?.tool, callID: input?.id },
        { args: structuredClone(input?.input ?? {}) },
      ),
    )
    await register(ctx?.tool, "execute.after", async (input) => {
      if (input?.status === "completed") {
        await hooks["tool.execute.after"](
          { sessionID: input.sessionID, tool: input.tool, callID: input.id, args: input.input ?? {} },
          v2ToolResponse(input.result),
        )
        return
      }
      const now = Date.now()
      await hooks.event({
        event: {
          type: "message.part.updated",
          properties: {
            sessionID: input?.sessionID,
            part: {
              id: `${input?.messageID}:tool:${input?.id}`,
              messageID: input?.messageID,
              sessionID: input?.sessionID,
              callID: input?.id,
              type: "tool",
              tool: input?.tool,
              state: {
                status: "error",
                input: input?.input ?? {},
                error: input?.error?.message || String(input?.error ?? "tool failed"),
                time: { start: now, end: now },
              },
            },
          },
        },
      })
    })

    const controller = new AbortController()
    const consuming = (async () => {
      if (typeof ctx?.event?.subscribe !== "function") return
      for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
        if (controller.signal.aborted) break
        for (const translated of v2Events(event, models)) {
          try {
            await hooks.event({ event: translated })
          } catch (err) {
            await debugLog(ctx, "Beacon V2 event failed", { type: translated.type, error: String(err) })
          }
        }
        if (event?.type === "session.deleted") models.delete(event?.data?.sessionID)
      }
    })().catch((err) => {
      if (!controller.signal.aborted) void debugLog(ctx, "Beacon V2 event subscription failed", { error: String(err) })
    })

    return async () => {
      controller.abort()
      await consuming
      await Promise.all(registrations.map((item) => Promise.resolve(item.dispose?.()).catch(() => undefined)))
    }
  },
}
