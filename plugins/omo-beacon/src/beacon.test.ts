import { afterEach, describe, expect, test } from "bun:test"

import { createBeaconExtension } from "./beacon"

const senderKey = Symbol.for("beacon.omo.testSender")

type Sent = Record<string, unknown>

function captureSends(): Sent[] {
  const sent: Sent[] = []
  ;(globalThis as Record<symbol, unknown>)[senderKey] = (payload: Sent) => {
    sent.push(payload)
  }
  return sent
}

// A stand-in for Senpi's ExtensionAPI that records subscriptions and lets a test fire one.
function fakePi() {
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
    sessionManager: { getSessionId: () => "sess-1" },
    ...overrides,
  }
}

afterEach(() => {
  delete (globalThis as Record<symbol, unknown>)[senderKey]
})

describe("beacon omo (senpi) extension", () => {
  // These strings are the contract between this extension and the omo-event mapper in the hook
  // adapter. A typo on either side produces no telemetry rather than an error, so the list is
  // pinned here and asserted against the mapper's own list on the Go side.
  test("subscribes to exactly the events the mapper handles", () => {
    const pi = fakePi()
    createBeaconExtension().register(pi.api)

    expect([...pi.handlers.keys()].sort()).toEqual([
      "input",
      "message_end",
      "session_shutdown",
      "session_start",
      "tool_call",
      "tool_result",
      "user_bash",
    ])
  })

  // Senpi's context hands the session id through an accessor, not as a bare field, so the envelope
  // has to carry it or every event loses the field that groups a run.
  test("lifts session identity off the context onto the envelope", async () => {
    const sent = captureSends()
    const pi = fakePi()
    createBeaconExtension().register(pi.api)

    await pi.fire({ type: "session_start", reason: "startup" }, context())

    expect(sent[0].sessionId).toBe("sess-1")
    expect(sent[0].cwd).toBe("/repo")
  })

  // Re-read per event rather than captured at load: `/new`, a resume, a fork and `/tree` all
  // replace the session without reloading the extension, so a cached id would be silently wrong.
  test("re-reads the session id for every event", async () => {
    const sent = captureSends()
    const pi = fakePi()
    createBeaconExtension().register(pi.api)

    let current = "sess-first"
    const ctx = context({ sessionManager: { getSessionId: () => current } })

    await pi.fire({ type: "session_start", reason: "startup" }, ctx)
    current = "sess-second"
    await pi.fire({ type: "session_start", reason: "new" }, ctx)

    expect(sent.map((event) => event.sessionId)).toEqual(["sess-first", "sess-second"])
  })

  test("joins provider and model id into one model string", async () => {
    const sent = captureSends()
    const pi = fakePi()
    createBeaconExtension().register(pi.api)

    await pi.fire(
      { type: "input", text: "hello" },
      context({ model: { id: "claude-opus-5", provider: "anthropic" } }),
    )

    expect(sent[0].model).toBe("anthropic/claude-opus-5")
  })

  // A throwing accessor must cost the field, not the event. An extension that drops telemetry
  // because one getter failed is worse than one that reports an event without its session id.
  test("survives a context whose accessors throw", async () => {
    const sent = captureSends()
    const pi = fakePi()
    createBeaconExtension().register(pi.api)

    await pi.fire(
      { type: "tool_call", toolName: "bash", toolCallId: "call-1" },
      {
        sessionManager: {
          getSessionId: () => {
            throw new Error("session gone")
          },
        },
      },
    )

    expect(sent).toHaveLength(1)
    expect(sent[0].type).toBe("tool_call")
    expect(sent[0].sessionId).toBeUndefined()
  })

  test("survives a missing context entirely", async () => {
    const sent = captureSends()
    const pi = fakePi()
    createBeaconExtension().register(pi.api)

    await pi.fire({ type: "session_shutdown", reason: "quit" })

    expect(sent).toHaveLength(1)
    expect(sent[0].type).toBe("session_shutdown")
  })

  // Senpi's session events carry an AbortSignal and its message events carry live AgentMessage
  // objects, so a payload JSON.stringify would reject is the normal case, not an edge case.
  test("serializes an event containing a cycle", async () => {
    const sent = captureSends()
    const pi = fakePi()
    createBeaconExtension().register(pi.api)

    const selfReferential: Record<string, unknown> = { name: "loop" }
    selfReferential.self = selfReferential

    await pi.fire({ type: "tool_result", toolName: "bash", details: selfReferential }, context())

    expect(sent).toHaveLength(1)
    expect(JSON.stringify(sent[0])).toContain("tool_result")
  })

  // Every handler must resolve to undefined. Senpi reads a returned object as a request to change
  // behavior on several of these events -- `{ block: true }` on tool_call, `{ action: "transform" }`
  // on input -- so a handler that returned anything would turn this observer into an enforcer.
  test("handlers return nothing so Senpi never reads a directive", async () => {
    captureSends()
    const pi = fakePi()
    createBeaconExtension().register(pi.api)

    for (const [, handler] of pi.handlers) {
      const result = await handler({ type: "tool_call", toolName: "bash" }, context())
      expect(result).toBeUndefined()
    }
  })

  // Event fields win over the lifted identity fields on a key collision, so a value Senpi reported
  // is never overwritten by one Beacon derived.
  test("event fields take precedence over lifted identity", async () => {
    const sent = captureSends()
    const pi = fakePi()
    createBeaconExtension().register(pi.api)

    await pi.fire({ type: "user_bash", command: "ls", cwd: "/other" }, context())

    expect(sent[0].cwd).toBe("/other")
  })

  // Senpi exposes no `hasUI` the way Prime Agent renamed it -- both mean the same thing (whether the
  // session had a user-facing surface at all), recorded under this runtime's own key so the two are
  // never read as one field by a query that groups by harness.
  test("records whether the session had a user-facing surface", async () => {
    const sent = captureSends()
    const pi = fakePi()
    createBeaconExtension().register(pi.api)

    await pi.fire({ type: "input", text: "hello" }, context({ hasUI: false }))
    await pi.fire({ type: "input", text: "hello" }, context())

    expect(sent[0].omoHasUi).toBe(false)
    // Absent rather than defaulted: a host that did not report it should not be recorded as one
    // that reported "no".
    expect(sent[1].omoHasUi).toBeUndefined()
  })
})
