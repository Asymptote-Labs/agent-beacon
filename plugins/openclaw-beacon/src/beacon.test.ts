import { afterEach, describe, expect, test } from "bun:test"

import entry, { createBeaconPlugin } from "./beacon.js"

const senderKey = Symbol.for("beacon.openclaw.testSender")

type Sent = Record<string, unknown>

function captureSends(): Sent[] {
  const sent: Sent[] = []
  ;(globalThis as Record<symbol, unknown>)[senderKey] = (payload: Sent) => {
    sent.push(payload)
  }
  return sent
}

type Handler = (event: Record<string, unknown>, ctx: unknown) => unknown

// A stand-in for OpenClaw's plugin API that records registrations and lets a test fire one.
function fakeGateway() {
  const handlers = new Map<string, Handler>()
  return {
    api: {
      on(hook: string, handler: Handler) {
        handlers.set(hook, handler)
      },
    },
    handlers,
    async fire(hook: string, event: Record<string, unknown>, ctx?: unknown) {
      const handler = handlers.get(hook)
      if (!handler) throw new Error(`no handler registered for ${hook}`)
      return await handler(event, ctx)
    },
    // fireSync returns whatever the handler returned without awaiting, which is how a test
    // observes that a handler did not hand OpenClaw a promise to wait on.
    fireSync(hook: string, event: Record<string, unknown>, ctx?: unknown) {
      const handler = handlers.get(hook)
      if (!handler) throw new Error(`no handler registered for ${hook}`)
      return handler(event, ctx)
    },
  }
}

function context(overrides: Record<string, unknown> = {}) {
  return {
    sessionId: "sess-1",
    sessionKey: "discord:chan-9",
    runId: "run-7",
    agentId: "main",
    workspaceDir: "/workspace/repo",
    modelProviderId: "openai",
    modelId: "gpt-5",
    channel: "discord",
    ...overrides,
  }
}

afterEach(() => {
  delete (globalThis as Record<symbol, unknown>)[senderKey]
  // The pending-paths cache is module state, so a test that leaves an entry behind would change
  // the next test's result. A session_end clears it through the plugin's own path.
  const gateway = fakeGateway()
  createBeaconPlugin().register(gateway.api)
  void gateway.fire("session_end", { sessionId: "cleanup" }, context())
})

describe("beacon openclaw plugin", () => {
  // These strings are the contract between this plugin and the openclaw-event mapper in the hook
  // adapter. A typo on either side produces no telemetry rather than an error, so the list is
  // pinned here and asserted against the mapper's own list on the Go side.
  test("registers exactly the hooks the mapper handles", () => {
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    expect([...gateway.handlers.keys()].sort()).toEqual([
      "after_compaction",
      "after_tool_call",
      "before_compaction",
      "before_tool_call",
      "llm_output",
      "message_received",
      "session_end",
      "session_start",
      "subagent_ended",
      "subagent_spawned",
    ])
  })

  // Registering any of these would ask OpenClaw for a power Beacon must not hold: rewriting a
  // prompt, claiming a message before the agent sees it, or voting on whether a skill installs.
  test("never registers a prompt-injection, claim, or evaluate hook", () => {
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    for (const forbidden of [
      "before_prompt_build",
      "agent_turn_prepare",
      "heartbeat_prompt_contribution",
      "inbound_claim",
      "before_dispatch",
      "reply_dispatch",
      "before_agent_reply",
      "skill_proposal_evaluate",
      "before_install",
      "message_sending",
      "reply_payload_sending",
      "tool_result_persist",
      "before_message_write",
    ]) {
      expect(gateway.handlers.has(forbidden)).toBe(false)
    }
  })

  test("the default export is a loadable plugin entry", () => {
    const gateway = fakeGateway()
    expect(entry.id).toBe("beacon-endpoint")
    entry.register(gateway.api)
    expect(gateway.handlers.size).toBe(10)
  })

  test("lifts session, run, workspace, and model identity onto the envelope", async () => {
    const sent = captureSends()
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    await gateway.fire("session_start", { sessionId: "sess-1" }, context())

    expect(sent).toHaveLength(1)
    expect(sent[0]).toMatchObject({
      hook: "session_start",
      sessionId: "sess-1",
      sessionKey: "discord:chan-9",
      runId: "run-7",
      agentId: "main",
      channel: "discord",
      cwd: "/workspace/repo",
      model: "openai/gpt-5",
    })
  })

  // A chat turn that never touched a repository genuinely has no workspace. Defaulting to the
  // gateway's own directory would attribute the work to wherever the daemon was started.
  test("omits the workspace when OpenClaw reports none", async () => {
    const sent = captureSends()
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    await gateway.fire("session_start", { sessionId: "sess-1" }, context({ workspaceDir: undefined }))

    expect(sent[0]).not.toHaveProperty("cwd")
  })

  test("reports a model with no provider prefix as its bare id", async () => {
    const sent = captureSends()
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    await gateway.fire("session_start", { sessionId: "sess-1" }, context({ modelProviderId: undefined }))

    expect(sent[0]?.model).toBe("gpt-5")
  })

  // The reason the hook event is nested rather than spread: `params` keys belong to the model.
  test("a tool argument cannot overwrite the envelope's identity", async () => {
    const sent = captureSends()
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    await gateway.fire(
      "after_tool_call",
      {
        toolName: "exec",
        toolCallId: "call-1",
        params: { command: "echo hi", sessionId: "attacker", cwd: "/etc", hook: "session_end" },
      },
      context(),
    )

    expect(sent[0]?.sessionId).toBe("sess-1")
    expect(sent[0]?.cwd).toBe("/workspace/repo")
    expect(sent[0]?.hook).toBe("after_tool_call")
    expect((sent[0]?.event as Record<string, unknown>).params).toMatchObject({ command: "echo hi" })
  })

  // OpenClaw registers before_tool_call fail-closed: a handler that throws or exceeds its timeout
  // blocks the tool call. This handler must therefore never hand OpenClaw something to wait on.
  test("before_tool_call returns synchronously without a promise", () => {
    captureSends()
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    const returned = gateway.fireSync(
      "before_tool_call",
      { toolName: "exec", toolCallId: "call-1", params: { command: "ls" } },
      context(),
    )

    expect(returned).toBeUndefined()
  })

  // Every observe handler must resolve to undefined: OpenClaw reads a returned object on its
  // modify-kind hooks as a request to change behavior.
  test("observe handlers resolve to undefined", async () => {
    captureSends()
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    for (const hook of ["session_start", "after_tool_call", "llm_output", "message_received"]) {
      expect(await gateway.fire(hook, {}, context())).toBeUndefined()
    }
  })

  // apply_patch takes one opaque patch string, so without the paths carried forward from the
  // proposal a completed patch produces no file rows at all.
  test("carries derivedPaths forward from before_tool_call to after_tool_call", async () => {
    const sent = captureSends()
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    await gateway.fire(
      "before_tool_call",
      {
        toolName: "apply_patch",
        toolCallId: "call-42",
        params: { input: "*** Begin Patch\n*** End Patch\n" },
        derivedPaths: ["/workspace/repo/a.go", "/workspace/repo/b.go"],
      },
      context(),
    )
    await gateway.fire(
      "after_tool_call",
      { toolName: "apply_patch", toolCallId: "call-42", params: { input: "*** Begin Patch\n*** End Patch\n" } },
      context(),
    )

    const completion = sent.at(-1)?.event as Record<string, unknown>
    expect(completion.derivedPaths).toEqual(["/workspace/repo/a.go", "/workspace/repo/b.go"])
  })

  // A patch event enriched with another call's paths would read as evidence that those files
  // changed. Absent is the only safe answer.
  test("does not attach paths from a different tool call", async () => {
    const sent = captureSends()
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    await gateway.fire(
      "before_tool_call",
      { toolName: "apply_patch", toolCallId: "call-1", params: {}, derivedPaths: ["/a.go"] },
      context(),
    )
    await gateway.fire("after_tool_call", { toolName: "apply_patch", toolCallId: "call-2", params: {} }, context())

    const completion = sent.at(-1)?.event as Record<string, unknown>
    expect(completion.derivedPaths).toBeUndefined()
  })

  // The paths are consumed by the completion they belong to, so a second completion for the same
  // id cannot pick them up again.
  test("forgets derivedPaths once their call completes", async () => {
    const sent = captureSends()
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    await gateway.fire(
      "before_tool_call",
      { toolName: "apply_patch", toolCallId: "call-1", params: {}, derivedPaths: ["/a.go"] },
      context(),
    )
    await gateway.fire("after_tool_call", { toolName: "apply_patch", toolCallId: "call-1", params: {} }, context())
    await gateway.fire("after_tool_call", { toolName: "apply_patch", toolCallId: "call-1", params: {} }, context())

    expect((sent.at(-2)?.event as Record<string, unknown>).derivedPaths).toEqual(["/a.go"])
    expect((sent.at(-1)?.event as Record<string, unknown>).derivedPaths).toBeUndefined()
  })

  // An event that names its own session is more specific than the handler context, which is the
  // gateway's more general answer.
  test("an event's own session identity wins over the context's", async () => {
    const sent = captureSends()
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    await gateway.fire("session_end", { sessionId: "sess-9", reason: "idle" }, context())

    expect(sent[0]?.sessionId).toBe("sess-9")
  })

  // A payload carrying a cycle, a bigint, or an AbortSignal must not cost the whole event.
  test("serializes payloads that JSON.stringify would reject", async () => {
    const sent = captureSends()
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    const cyclic: Record<string, unknown> = { toolName: "exec", params: { command: "ls" } }
    cyclic.self = cyclic
    await gateway.fire("after_tool_call", cyclic, {
      ...context(),
      abortSignal: new AbortController().signal,
      tokens: 10n,
    })

    const event = sent[0]?.event as Record<string, unknown>
    expect(event.toolName).toBe("exec")
    expect(event.self).toBeUndefined()
    expect(() => JSON.stringify(sent[0])).not.toThrow()
  })

  // A handler that throws on before_tool_call blocks the tool call, so nothing in this plugin may
  // propagate an error -- not even a sender that fails outright.
  test("a failing sender never reaches the gateway", async () => {
    ;(globalThis as Record<symbol, unknown>)[senderKey] = () => {
      throw new Error("sender exploded")
    }
    const gateway = fakeGateway()
    createBeaconPlugin().register(gateway.api)

    expect(() =>
      gateway.fireSync("before_tool_call", { toolName: "exec", params: {} }, context()),
    ).not.toThrow()
    await expect(gateway.fire("session_start", { sessionId: "sess-1" }, context())).resolves.toBeUndefined()
  })
})
