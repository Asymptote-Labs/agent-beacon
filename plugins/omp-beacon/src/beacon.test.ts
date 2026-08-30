import { afterEach, describe, expect, test } from "bun:test"

import { createBeaconExtension } from "./beacon"

const senderKey = Symbol.for("beacon.omp.testSender")

type Sent = Record<string, unknown>

function captureSends(): Sent[] {
  const sent: Sent[] = []
  ;(globalThis as Record<symbol, unknown>)[senderKey] = (payload: Sent) => {
    sent.push(payload)
  }
  return sent
}

// A stand-in for Oh My Pi's ExtensionAPI that records subscriptions and lets a test fire one.
function fakeOmp() {
  const handlers = new Map<string, (event: Record<string, unknown>, ctx: unknown) => Promise<void> | void>()
  return {
    api: {
      on(event: string, handler: (event: Record<string, unknown>, ctx: unknown) => Promise<void> | void) {
        handlers.set(event, handler)
      },
    },
    handlers,
    async fire(event: Record<string, unknown> & { type: string }, ctx?: unknown) {
      const handler = handlers.get(event.type)
      if (!handler) throw new Error(`no handler registered for ${event.type}`)
      await handler(event, ctx)
    },
  }
}

function context(overrides: Record<string, unknown> = {}) {
  return {
    cwd: "/repo",
    mode: "tui",
    sessionManager: { getSessionId: () => "sess-1", getCwd: () => "/repo" },
    ...overrides,
  }
}

afterEach(() => {
  delete (globalThis as Record<symbol, unknown>)[senderKey]
})

describe("beacon oh my pi extension", () => {
  // These strings are the contract between this extension and the omp-event mapper in the hook
  // adapter. A typo on either side produces no telemetry rather than an error, so the list is
  // pinned here and asserted against the mapper's own list on the Go side.
  test("subscribes to exactly the events the mapper handles", () => {
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    expect([...omp.handlers.keys()].sort()).toEqual([
      "input",
      "message_end",
      "session_shutdown",
      "session_start",
      "tool_approval_requested",
      "tool_approval_resolved",
      "tool_call",
      "tool_result",
      "user_bash",
      "user_python",
    ])
  })

  // The reason this extension exists rather than pointing the Pi one at a different directory.
  // These are decisions an operator was actually asked to make, which Beacon refuses to synthesize
  // on runtimes that do not report them.
  test("subscribes to the approval events upstream Pi does not have", () => {
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    expect(omp.handlers.has("tool_approval_requested")).toBe(true)
    expect(omp.handlers.has("tool_approval_resolved")).toBe(true)
  })

  // Oh My Pi publishes upwards of thirty events, most of them provider-request and TUI internals.
  // Subscribing to one would put Beacon in the path of every streaming token update.
  test("does not subscribe to streaming or provider internals", () => {
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    for (const noisy of [
      "message_update",
      "message_start",
      "before_provider_request",
      "after_provider_response",
      "context",
      "tool_execution_update",
      "tool_execution_start",
      "auto_compaction_start",
      "auto_retry_start",
      "ttsr_triggered",
      "todo_reminder",
    ]) {
      expect(omp.handlers.has(noisy)).toBe(false)
    }
  })

  // mcp_notification fires for every JSON-RPC notification a connected server sends, most of them
  // routine list refreshes. It is MCP transport plumbing, not an action the agent took.
  test("does not subscribe to mcp notifications", () => {
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    expect(omp.handlers.has("mcp_notification")).toBe(false)
  })

  test("forwards the event with its type intact", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "input", text: "do the thing", source: "interactive" }, context())

    expect(sent).toHaveLength(1)
    expect(sent[0].type).toBe("input")
    expect(sent[0].text).toBe("do the thing")
  })

  test("forwards an approval decision with its outcome intact", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire(
      {
        type: "tool_approval_resolved",
        sessionId: "sess-1",
        toolCallId: "call-1",
        toolName: "bash",
        approved: false,
        reason: "operator declined",
      },
      context(),
    )

    expect(sent[0].approved).toBe(false)
    expect(sent[0].toolCallId).toBe("call-1")
    expect(sent[0].reason).toBe("operator declined")
  })

  test("forwards operator python with its source intact", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire(
      { type: "user_python", code: "import os", excludeFromContext: true, cwd: "/repo" },
      context(),
    )

    expect(sent[0].code).toBe("import os")
    expect(sent[0].excludeFromContext).toBe(true)
  })

  // Oh My Pi keeps identity on the handler context behind accessor functions, not on the event, so
  // the envelope has to carry it or every event loses the field that groups a run.
  test("lifts session identity off the context onto the envelope", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "session_start" }, context())

    expect(sent[0].sessionId).toBe("sess-1")
    expect(sent[0].cwd).toBe("/repo")
  })

  // A print or rpc run is unattended, which changes how an approval row should be read: there was
  // no human at the terminal to ask.
  test("records which surface the session is running on", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "session_start" }, context({ mode: "print" }))

    expect(sent[0].ompMode).toBe("print")
  })

  // Re-read per event rather than captured at load: `/new`, a resume, a fork and a tree navigation
  // all replace the session without reloading the extension, so a cached id would be silently
  // wrong afterwards.
  test("re-reads the session id for every event", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    let current = "sess-first"
    const ctx = context({ sessionManager: { getSessionId: () => current, getCwd: () => "/repo" } })

    await omp.fire({ type: "session_start" }, ctx)
    current = "sess-second"
    await omp.fire({ type: "session_start" }, ctx)

    expect(sent.map((event) => event.sessionId)).toEqual(["sess-first", "sess-second"])
  })

  test("joins provider and model id into one model string", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire(
      { type: "input", text: "hello" },
      context({ model: { id: "claude-opus-5", provider: "anthropic" } }),
    )

    expect(sent[0].model).toBe("anthropic/claude-opus-5")
  })

  // A throwing accessor must cost the field, not the event. An extension that drops telemetry
  // because one getter failed is worse than one that reports an event without its cwd.
  test("survives a context whose accessors throw", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "tool_call", toolName: "bash", toolCallId: "call-1" }, {
      sessionManager: {
        getSessionId: () => {
          throw new Error("session gone")
        },
        getCwd: () => {
          throw new Error("cwd gone")
        },
      },
    })

    expect(sent).toHaveLength(1)
    expect(sent[0].type).toBe("tool_call")
    expect(sent[0].sessionId).toBeUndefined()
  })

  test("survives a missing context entirely", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "session_shutdown" })

    expect(sent).toHaveLength(1)
    expect(sent[0].type).toBe("session_shutdown")
  })

  // Oh My Pi's session events carry an AbortSignal and its message events carry live AgentMessage
  // objects, so a payload JSON.stringify would reject is the normal case, not the edge case.
  test("serializes an event containing a cycle", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    const selfReferential: Record<string, unknown> = { name: "loop" }
    selfReferential.self = selfReferential

    await omp.fire({ type: "tool_result", toolName: "read", details: selfReferential }, context())

    expect(sent).toHaveLength(1)
    expect(JSON.stringify(sent[0])).toContain("tool_result")
  })

  test("drops functions and stringifies bigints rather than failing the send", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire(
      {
        type: "tool_result",
        toolName: "bash",
        // eslint-disable-next-line @typescript-eslint/no-empty-function
        onDone: () => {},
        bytes: BigInt(42),
      },
      context(),
    )

    expect(sent[0].onDone).toBeUndefined()
    expect(sent[0].bytes).toBe("42")
  })

  // Every handler must resolve to undefined. Oh My Pi reads a returned object as a request to
  // change behavior -- `{ block: true }` or `{ input }` on tool_call, `{ result }` on user_bash and
  // user_python, `{ content }` on tool_result -- so a handler that returned anything would turn
  // this observer into an enforcer. On the approval events the stakes are highest: a returned value
  // there would let telemetry answer a question that was put to the operator.
  test("handlers return nothing so Oh My Pi never reads a directive", async () => {
    captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    for (const [type, handler] of omp.handlers) {
      const result = await handler({ type, toolName: "bash" }, context())
      expect(result).toBeUndefined()
    }
  })

  // Event fields win over the lifted identity fields on a key collision, so a value Oh My Pi
  // reported is never overwritten by one Beacon derived. user_bash and user_python carry their own
  // cwd; the approval events carry their own sessionId.
  test("event fields take precedence over lifted identity", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "user_bash", command: "ls", cwd: "/other" }, context())
    await omp.fire(
      { type: "tool_approval_requested", sessionId: "sess-own", toolName: "bash" },
      context(),
    )

    expect(sent[0].cwd).toBe("/other")
    expect(sent[1].sessionId).toBe("sess-own")
  })
})
