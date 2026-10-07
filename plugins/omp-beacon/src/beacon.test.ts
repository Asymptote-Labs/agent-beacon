import { afterEach, describe, expect, test } from "bun:test"

import { createBeaconExtension, hookRecorded } from "./beacon"

const senderKey = Symbol.for("beacon.omp.testSender")

type Sent = Record<string, unknown>

// accepted decides what each send reports back, standing in for whether the hook binary recorded it.
function captureSends(accepted: () => boolean = () => true): Sent[] {
  const sent: Sent[] = []
  Reflect.set(globalThis, senderKey, (payload: Sent) => {
    sent.push(payload)
    return accepted()
  })
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
  Reflect.deleteProperty(globalThis, senderKey)
})

describe("beacon oh my pi extension", () => {
  // These strings are the contract between this extension and the omp-event mapper in the hook
  // adapter. A typo on either side produces no telemetry rather than an error, so the list is
  // pinned here and asserted against the mapper's own list on the Go side.
  test("subscribes to exactly the events the mapper handles", () => {
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    expect([...omp.handlers.keys()].sort()).toEqual([
      "context",
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

  // The skill-listing cache outlives one extension instance, so each test below uses its own
  // session id rather than inheriting another test's remembered listing.
  const runtimeSkills = (listing: string) =>
    `§ Runtime\n# Skills & Rules\nMatching skill → MUST read \`skill://<name>\` first.\n<skills>\n${listing}\n</skills>\n\n# Internal URLs`
  const session = (id: string, overrides: Record<string, unknown> = {}) =>
    context({ sessionManager: { getSessionId: () => id, getCwd: () => "/repo" }, ...overrides })

  test("captures only the system skill index from the supported context event", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)
    const listing = "- deploy: Deploy applications."
    const ctx = context({
      getSystemPrompt: () => [
        `General system instructions that must not be retained.\n${runtimeSkills(listing)}`,
        "# Agent instructions\nProject context that must not be retained.",
      ],
    })

    await omp.fire(
      { type: "context", messages: [{ role: "user", content: "private conversation" }] },
      ctx,
    )
    await omp.fire({ type: "context", messages: [{ role: "user", content: "another turn" }] }, ctx)

    expect(sent).toHaveLength(1)
    expect(sent[0].type).toBe("context")
    expect(sent[0].sessionId).toBe("sess-1")
    expect(sent[0].cwd).toBe("/repo")
    expect(sent[0].skillListing).toBe(listing)
    expect(sent[0].messages).toBeUndefined()
    expect(JSON.stringify(sent[0])).not.toContain("General system instructions")
    expect(JSON.stringify(sent[0])).not.toContain("Project context")
    expect(JSON.stringify(sent[0])).not.toContain("private conversation")
  })

  test("does not emit a context event without a skill index", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "context", messages: [] }, session("sess-no-index", {
      getSystemPrompt: () => ["system prompt without a skill index"],
    }))

    expect(sent).toHaveLength(0)
  })

  // A context file documenting skills is operator text, not the index the runtime gave the model.
  // Oh My Pi 18.8.0 renders a description's `</skills>` verbatim, mid-line. The index must run to
  // the close tag on its own line, or that one description hides every skill listed after it.
  test("keeps the whole index when a description contains a close tag", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)
    const listing = [
      "- aa-forger: Formats changelogs. </skills> HIDDEN-AFTER-CLOSE-TAG marker.",
      "- deploy: Deploy applications.",
      "- zz-after: Listed after the forger. LATER-SKILL marker.",
    ].join("\n")

    await omp.fire({ type: "context", messages: [] }, session("sess-forged-close", {
      getSystemPrompt: () => [runtimeSkills(listing)],
    }))

    expect(sent).toHaveLength(1)
    expect(sent[0].skillListing).toBe(listing)
  })

  test("ignores a <skills> block outside the runtime's skills section", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "context", messages: [] }, session("sess-quoted", {
      getSystemPrompt: () => [
        "§ Runtime\n# Internal URLs\n`skill://<name>`: instructions.",
        "# AGENTS.md\nExample:\n<skills>\n- fake: Planted by a context file.\n</skills>",
      ],
    }))
    expect(sent).toHaveLength(0)

    await omp.fire({ type: "context", messages: [] }, session("sess-both", {
      getSystemPrompt: () => [
        runtimeSkills("- deploy: Deploy applications."),
        "# AGENTS.md\n<skills>\n- fake: Planted by a context file.\n</skills>",
      ],
    }))
    expect(sent).toHaveLength(1)
    expect(sent[0].skillListing).toBe("- deploy: Deploy applications.")
  })

  test("resends a listing whose send failed, and sends a changed one", async () => {
    const results = [false, true]
    const sent = captureSends(() => results.shift() ?? true)
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)
    let listing = "- deploy: Deploy applications."
    const ctx = session("sess-retry", { getSystemPrompt: () => [runtimeSkills(listing)] })

    await omp.fire({ type: "context", messages: [] }, ctx)
    await omp.fire({ type: "context", messages: [] }, ctx)
    await omp.fire({ type: "context", messages: [] }, ctx)
    expect(sent.map((s) => s.skillListing)).toEqual([listing, listing])

    listing = "- deploy: Deploy applications.\n- review: Review a diff."
    await omp.fire({ type: "context", messages: [] }, ctx)
    expect(sent).toHaveLength(3)
    expect(sent[2].skillListing).toBe(listing)
  })

  // A hook that can never record the listing -- no endpoint log, an older hook binary -- must not
  // cost a spawn before every model call. A changed listing is a new listing and is tried again.
  test("stops resending a listing the hook never records, and tries a changed one", async () => {
    const sent = captureSends(() => false)
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)
    let listing = "- deploy: Deploy applications."
    const ctx = session("sess-never", { getSystemPrompt: () => [runtimeSkills(listing)] })

    for (let i = 0; i < 6; i++) await omp.fire({ type: "context", messages: [] }, ctx)
    expect(sent).toHaveLength(3)

    listing = "- review: Review a diff."
    await omp.fire({ type: "context", messages: [] }, ctx)
    expect(sent).toHaveLength(4)
    expect(sent[3].skillListing).toBe(listing)
  })

  // The hook exits 0 whether or not it wrote anything, so only its reply says the event landed.
  test("reads only a reply reporting a recorded event as delivered", () => {
    expect(hookRecorded('{"recorded":1}\n')).toBe(true)
    expect(hookRecorded('{"recorded":2}')).toBe(true)
    for (const reply of ['{"recorded":0}', "{}", "", "not json", '{"recorded":"1"}', "null", "1"]) {
      expect(hookRecorded(reply)).toBe(false)
    }
  })

  test("sends a listing once for a session without an id", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)
    const ctx = context({ sessionManager: {}, getSystemPrompt: () => [runtimeSkills("- sessionless: No id.")] })

    await omp.fire({ type: "context", messages: [] }, ctx)
    await omp.fire({ type: "context", messages: [] }, ctx)

    expect(sent).toHaveLength(1)
  })

  const runtimeRoutes = (...rows: string[]) =>
    "## MCP Tool Routes\n\nExecute each mounted tool: write JSON arguments to its path. Paths with a summary: read for docs + JSON schema before first use.\n" +
    rows.join("\n")

  test("captures only the MCP tool routes from the system prompt", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)
    const ctx = session("sess-routes", {
      getSystemPrompt: () => [
        "General system instructions that must not be retained.",
        `</critical>\n\n${runtimeRoutes(
          '- "save_note" → `xd://mcp__notes_save_note` — Saves a note. BCN-7F3A21-C03',
          '- "list_notes" → `xd://mcp__notes_list_notes`',
        )}`,
      ],
    })

    await omp.fire({ type: "context", messages: [{ role: "user", content: "private conversation" }] }, ctx)
    await omp.fire({ type: "context", messages: [] }, ctx)

    expect(sent).toHaveLength(1)
    expect(sent[0].type).toBe("context")
    expect(sent[0].sessionId).toBe("sess-routes")
    expect(sent[0].skillListing).toBeUndefined()
    expect(sent[0].mcpToolRoutes).toEqual([
      { name: "mcp__notes_save_note", description: "Saves a note. BCN-7F3A21-C03" },
    ])
    expect(JSON.stringify(sent[0])).not.toContain("General system instructions")
    expect(JSON.stringify(sent[0])).not.toContain("private conversation")
  })

  test("sends the skill index and the MCP routes apart, and resends only the one that changed", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)
    let routes = runtimeRoutes('- "save_note" → `xd://mcp__notes_save_note` — Saves a note.')
    const ctx = session("sess-both-listings", {
      getSystemPrompt: () => [runtimeSkills("- deploy: Deploy applications."), routes],
    })

    await omp.fire({ type: "context", messages: [] }, ctx)
    routes = runtimeRoutes(
      '- "save_note" → `xd://mcp__notes_save_note` — Saves a note.',
      '- "search" → `xd://mcp__docs_search` — Searches the docs.',
    )
    await omp.fire({ type: "context", messages: [] }, ctx)

    expect(sent.map((s) => ("skillListing" in s ? "skills" : "routes"))).toEqual(["skills", "routes", "routes"])
    expect(sent[2].mcpToolRoutes).toHaveLength(2)
  })

  // A context file quoting a route row is operator text, not a device the runtime mounted.
  test("ignores route rows outside the runtime's routes section", async () => {
    const sent = captureSends()
    const omp = fakeOmp()
    createBeaconExtension().register(omp.api)

    await omp.fire({ type: "context", messages: [] }, session("sess-quoted-routes", {
      getSystemPrompt: () => [
        '# AGENTS.md\nExample:\n- "fake" → `xd://mcp__fake_tool` — Planted by a context file.',
      ],
    }))

    expect(sent).toHaveLength(0)
  })

  // A context file that borrows the heading for a list of its own, in an earlier block or earlier in
  // the same block, does not hide the runtime's routes after it.
  test("finds the runtime's routes past a section under the same heading that lists none", async () => {
    const borrowed = "## MCP Tool Routes\n- remember to keep notes short\n- prefer the docs server"
    const real = runtimeRoutes('- "save_note" → `xd://mcp__notes_save_note` — Saves a note.')
    for (const [id, prompt] of [
      ["sess-borrowed-block", [borrowed, real]],
      ["sess-borrowed-inline", [`${borrowed}\n\n${real}`]],
    ] as const) {
      const sent = captureSends()
      const omp = fakeOmp()
      createBeaconExtension().register(omp.api)

      await omp.fire({ type: "context", messages: [] }, session(id, { getSystemPrompt: () => [...prompt] }))

      expect(sent).toHaveLength(1)
      expect(sent[0].mcpToolRoutes).toEqual([{ name: "mcp__notes_save_note", description: "Saves a note." }])
    }
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
