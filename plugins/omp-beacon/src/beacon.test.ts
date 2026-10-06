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

  // Print and ACP never emit `input`, so the user message Oh My Pi delivers is the only record of
  // the prompt. Only its role and text leave the extension.
  test("forwards a print prompt from its user message", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire(
      {
        type: "message_end",
        message: {
          role: "user",
          attribution: "user",
          content: [
            { type: "text", text: "summarize the changes" },
            { type: "image", data: "aW1hZ2UtYnl0ZXM=", mimeType: "image/png" },
          ],
          timestamp: 1,
        },
      },
      context({ mode: "print" }),
    )

    expect(sent).toEqual([
      {
        cwd: "/repo",
        ompMode: "print",
        sessionId: "sess-1",
        type: "message_end",
        message: { role: "user" },
        prompt: "summarize the changes",
      },
    ])
  })

  // ACP reports `ctx.mode` "rpc" like an RPC client, but never emits `input`.
  test("forwards an ACP prompt, which arrives with no input event", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "message_end", message: { role: "user", content: "fix the test" } }, context({ mode: "rpc" }))

    expect(sent.map((event) => event.prompt)).toEqual(["fix the test"])
  })

  test("does not record an RPC client's prompt twice", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)
    const ctx = context({ mode: "rpc" })

    await omp.fire({ type: "input", text: "fix the test", source: "rpc" }, ctx)
    await omp.fire({ type: "message_end", message: { role: "user", content: "fix the test" } }, ctx)

    expect(sent.map((event) => event.type)).toEqual(["input"])
  })

  // An image-only submission still shows the session reports its prompts as `input`.
  test("an input without text still marks the session as reporting input", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)
    const ctx = context({ mode: "rpc" })

    await omp.fire({ type: "input", text: "first", source: "rpc" }, ctx)
    await omp.fire({ type: "input", text: "", source: "rpc" }, ctx)
    await omp.fire({ type: "message_end", message: { role: "user", content: "first" } }, ctx)

    expect(sent.map((event) => event.type)).toEqual(["input", "input"])
  })

  // Every prompt typed in the terminal arrives as `input`. A user message without one was sent by
  // an extension, so it is not recorded as a prompt even before the first `input`.
  test("never records an interactive user message as a prompt", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "message_end", message: { role: "user", content: "sent by an extension" } }, context())
    await omp.fire({ type: "input", text: "typed", source: "interactive" }, context())
    await omp.fire({ type: "message_end", message: { role: "user", content: "typed" } }, context())

    expect(sent.map((event) => event.type)).toEqual(["input"])
  })

  test("does not record runtime-generated or agent-handed messages as prompts", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)
    const ctx = context({ mode: "print" })

    await omp.fire({ type: "message_end", message: { role: "user", synthetic: true, content: "continue" } }, ctx)
    await omp.fire({ type: "message_end", message: { role: "user", attribution: "agent", content: "handoff" } }, ctx)
    await omp.fire({ type: "message_end", message: { role: "developer", content: "reminder" } }, ctx)

    expect(sent.filter((event) => "prompt" in event)).toEqual([])
  })

  test("does not record a subagent's task as a prompt", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire(
      { type: "message_end", message: { role: "user", content: "explore the repo" } },
      context({ mode: "print", agent: { kind: "sub" } }),
    )

    expect(sent).toEqual([])
  })

  test("judges a new session on its own input", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "input", text: "first", source: "rpc" }, context({ mode: "rpc" }))
    await omp.fire({ type: "session_start" }, context({ mode: "rpc" }))
    const next = context({ mode: "rpc", sessionManager: { getSessionId: () => "sess-2" } })
    await omp.fire({ type: "message_end", message: { role: "user", content: "second" } }, next)

    expect(sent.map((event) => event.type)).toEqual(["input", "session_start", "message_end"])
  })

  test("records repeated identical print prompts each time", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)
    const ctx = context({ mode: "print", sessionManager: undefined })

    await omp.fire({ type: "message_end", message: { role: "user", content: "repeat" } }, ctx)
    await omp.fire({ type: "message_end", message: { role: "user", content: "repeat" } }, ctx)

    expect(sent.map((event) => event.prompt)).toEqual(["repeat", "repeat"])
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

  // Oh My Pi's approval events name the tool and the call but carry none of its arguments. Every
  // approval detection Beacon ships matches on the command or the file path rather than on a tool
  // name, so an approval that said only "the operator denied bash" would be telemetry no rule could
  // act on. The extension carries the arguments across from the tool_call that proposed the call.
  test("carries the decided tool's arguments onto an approval", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire(
      { type: "tool_call", toolName: "bash", toolCallId: "call-1", input: { command: "rm -rf /var/data" } },
      context(),
    )
    await omp.fire(
      { type: "tool_approval_requested", toolName: "bash", toolCallId: "call-1", approvalMode: "always-ask" },
      context(),
    )
    await omp.fire(
      { type: "tool_approval_resolved", toolName: "bash", toolCallId: "call-1", approved: false },
      context(),
    )

    expect(sent[1].input).toEqual({ command: "rm -rf /var/data" })
    expect(sent[2].input).toEqual({ command: "rm -rf /var/data" })
  })

  // The join is the runtime's own call id, never a timestamp or a guess. An approval enriched with
  // some other call's arguments would be worse than one with none, because it would read as
  // evidence of a decision that was never made about it.
  test("never attaches another call's arguments to an approval", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire(
      { type: "tool_call", toolName: "bash", toolCallId: "call-1", input: { command: "ls" } },
      context(),
    )
    await omp.fire(
      { type: "tool_approval_requested", toolName: "write", toolCallId: "call-2" },
      context(),
    )

    expect(sent[1].input).toBeUndefined()
  })

  test("an approval with no call id is not enriched", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire(
      { type: "tool_call", toolName: "bash", toolCallId: "call-1", input: { command: "ls" } },
      context(),
    )
    await omp.fire({ type: "tool_approval_requested", toolName: "bash" }, context())

    expect(sent[1].input).toBeUndefined()
  })

  // A finished call is forgotten, so a later approval reusing an id cannot inherit stale arguments.
  test("forgets a tool call once it has finished", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire(
      { type: "tool_call", toolName: "bash", toolCallId: "call-1", input: { command: "ls" } },
      context(),
    )
    await omp.fire({ type: "tool_result", toolName: "bash", toolCallId: "call-1", isError: false }, context())
    await omp.fire({ type: "tool_approval_requested", toolName: "bash", toolCallId: "call-1" }, context())

    expect(sent[2].input).toBeUndefined()
  })

  // `/new`, a resume, a fork and a shutdown all end the run a pending call belonged to.
  test("forgets pending calls when the session ends or restarts", async () => {
    for (const boundary of ["session_start", "session_shutdown"]) {
      const sent = captureSends()
      const omp = fakeOmp()
      createBeaconExtension().register(omp.api)

      await omp.fire(
        { type: "tool_call", toolName: "bash", toolCallId: "call-1", input: { command: "ls" } },
        context(),
      )
      await omp.fire({ type: boundary }, context())
      await omp.fire({ type: "tool_approval_requested", toolName: "bash", toolCallId: "call-1" }, context())

      expect(sent[2].input).toBeUndefined()
    }
  })

  // The cache is bounded, so a run whose tool calls never finish -- an aborted session, a tool torn
  // down mid-flight -- cannot grow it for the life of the process.
  test("bounds how many in-flight tool calls it remembers", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    for (let i = 0; i < 200; i++) {
      await omp.fire(
        { type: "tool_call", toolName: "bash", toolCallId: `call-${i}`, input: { command: `echo ${i}` } },
        context(),
      )
    }
    // The oldest is evicted; the newest is still there.
    await omp.fire({ type: "tool_approval_requested", toolName: "bash", toolCallId: "call-0" }, context())
    await omp.fire({ type: "tool_approval_requested", toolName: "bash", toolCallId: "call-199" }, context())

    expect(sent[200].input).toBeUndefined()
    expect(sent[201].input).toEqual({ command: "echo 199" })
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
