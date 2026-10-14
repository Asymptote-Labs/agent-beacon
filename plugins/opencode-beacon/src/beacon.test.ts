import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import BeaconDefinition, { BeaconEndpointPlugin } from "./beacon"

const payloads: any[] = []
const senderKey = Symbol.for("beacon.opencode.testSender")

beforeEach(() => {
  payloads.length = 0
  ;(globalThis as any)[senderKey] = async (payload: any) => payloads.push(structuredClone(payload))
})

afterEach(() => {
  delete (globalThis as any)[senderKey]
})

async function plugin() {
  return BeaconEndpointPlugin({
    project: { id: "project-1" },
    directory: "/repo",
    worktree: "/repo",
    client: { app: { log: async () => true } },
  } as any)
}

describe("BeaconEndpointPlugin", () => {
  test("forwards prompts and correlated tool lifecycle payloads", async () => {
    const hooks = await plugin()

    await hooks.event!({
      event: { type: "session.created", properties: { sessionID: "ses_1", info: { id: "ses_1" } } },
    } as any)
    await hooks["chat.message"]!(
      { sessionID: "ses_1", model: { providerID: "moonshotai", modelID: "kimi-k3" } } as any,
      { parts: [{ type: "text", text: "summarize" }] } as any,
    )
    await hooks["tool.execute.before"]!(
      { tool: "bash", sessionID: "ses_1", callID: "call_1" },
      { args: { command: "git status --short" } },
    )
    await hooks["tool.execute.after"]!(
      { tool: "bash", sessionID: "ses_1", callID: "call_1", args: { command: "git status --short" } },
      { title: "status", output: "", metadata: { exitCode: 0 } },
    )

    expect(payloads.map((item) => item.type)).toEqual([
      "session.created",
      "chat.message",
      "tool.execute.before",
      "tool.execute.after",
    ])
    expect(payloads[3]).toMatchObject({
      session_id: "ses_1",
      call_id: "call_1",
      tool_name: "bash",
      directory: "/repo",
    })
  })

  test("forwards terminal text, errors, usage, and permission replies once", async () => {
    const hooks = await plugin()

    await hooks.event!({
      event: {
        type: "message.updated",
        properties: {
          sessionID: "ses_1",
          info: {
            id: "msg_1",
            role: "assistant",
            modelID: "kimi-k3",
            providerID: "moonshotai",
            time: { completed: 2 },
            tokens: { input: 3, output: 4, reasoning: 1, cache: { read: 2, write: 0 } },
          },
        },
      },
    } as any)
    await hooks.event!({
      event: {
        type: "message.part.updated",
        properties: {
          sessionID: "ses_1",
          part: {
            id: "prt_1",
            messageID: "msg_1",
            sessionID: "ses_1",
            type: "text",
            text: "Done",
            time: { start: 1, end: 2 },
          },
        },
      },
    } as any)
    await hooks.event!({
      event: {
        type: "permission.replied",
        properties: { sessionID: "ses_1", requestID: "per_1", reply: "reject" },
      },
    } as any)
    await hooks.event!({
      event: {
        type: "message.part.updated",
        properties: {
          sessionID: "ses_1",
          part: {
            id: "prt_tool",
            messageID: "msg_1",
            sessionID: "ses_1",
            callID: "call_failed",
            type: "tool",
            tool: "read",
            state: {
              status: "error",
              input: { filePath: "/repo/missing" },
              error: "not found",
              time: { start: 1, end: 2 },
            },
          },
        },
      },
    } as any)
    await Bun.sleep(20)

    expect(payloads.map((item) => item.type)).toEqual([
      "message.updated",
      "message.part.updated",
      "permission.replied",
      "message.part.updated",
    ])
    expect(payloads[1].model).toBe("moonshotai/kimi-k3")
    expect(payloads[3].part.state.status).toBe("error")
  })

  test("suppresses empty diffs and duplicate successful tool parts", async () => {
    const hooks = await plugin()
    await hooks["tool.execute.before"]!(
      { tool: "write", sessionID: "ses_1", callID: "call_1" },
      { args: { filePath: "/repo/test.txt", content: "value" } },
    )
    await hooks["tool.execute.after"]!(
      { tool: "write", sessionID: "ses_1", callID: "call_1", args: { filePath: "/repo/test.txt" } },
      { title: "write", output: "ok", metadata: {} },
    )
    await hooks.event!({
      event: { type: "session.diff", properties: { sessionID: "ses_1", diff: [] } },
    } as any)
    await hooks.event!({
      event: {
        type: "message.part.updated",
        properties: {
          sessionID: "ses_1",
          part: {
            id: "prt_1",
            messageID: "msg_1",
            sessionID: "ses_1",
            callID: "call_1",
            type: "tool",
            tool: "write",
            state: { status: "completed", input: {}, output: "ok", metadata: {}, time: { start: 1, end: 2 } },
          },
        },
      },
    } as any)
    await Bun.sleep(20)

    expect(payloads.map((item) => item.type)).toEqual(["tool.execute.before", "tool.execute.after"])
  })

  test("does not flush user text as assistant output and deduplicates session status", async () => {
    const hooks = await plugin()
    await hooks["chat.message"]!(
      { sessionID: "ses_1", model: { providerID: "moonshotai", modelID: "kimi-k3" } } as any,
      {
        message: { id: "msg_user", role: "user" },
        parts: [{ id: "prt_user", messageID: "msg_user", sessionID: "ses_1", type: "text", text: "hello" }],
      } as any,
    )
    await hooks.event!({
      event: {
        type: "message.part.updated",
        properties: {
          sessionID: "ses_1",
          part: { id: "prt_user", messageID: "msg_user", sessionID: "ses_1", type: "text", text: "hello" },
        },
      },
    } as any)
    for (const status of ["busy", "busy", "idle"]) {
      await hooks.event!({
        event: { type: "session.status", properties: { sessionID: "ses_1", status: { type: status } } },
      } as any)
    }
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } } as any)
    await Bun.sleep(20)

    expect(payloads.map((item) => item.type)).toEqual(["chat.message", "session.status", "session.status"])
    expect(payloads.some((item) => item.type === "message.part.updated")).toBe(false)
  })

  test("records shell deletion side effects and suppresses watcher duplicates", async () => {
    const hooks = await plugin()
    const path = "/repo/.tmp/beacon-opencode-e2e.txt"
    await hooks["tool.execute.before"]!(
      { tool: "bash", sessionID: "ses_1", callID: "call_rm" },
      { args: { command: `rm "${path}"` } },
    )
    await hooks.event!({
      event: { type: "file.watcher.updated", properties: { file: path, event: "unlink" } },
    } as any)
    await hooks["tool.execute.after"]!(
      { tool: "bash", sessionID: "ses_1", callID: "call_rm", args: { command: `rm "${path}"` } },
      { title: "rm", output: "", metadata: { exit: 0 } },
    )
    await Bun.sleep(20)

    expect(payloads.map((item) => item.type)).toEqual(["tool.execute.before", "tool.execute.after"])
    expect(payloads[1]).toMatchObject({
      type: "tool.execute.after",
      session_id: "ses_1",
      call_id: "call_rm",
      file_mutations: [{ path, operation: "delete" }],
    })
  })

  test("drains completed assistant parts when the message role arrives later", async () => {
    const hooks = await plugin()
    await hooks.event!({
      event: {
        type: "message.part.updated",
        properties: {
          sessionID: "ses_1",
          part: {
            id: "prt_late",
            messageID: "msg_late",
            sessionID: "ses_1",
            type: "text",
            text: "late role",
            time: { start: 1, end: 2 },
          },
        },
      },
    } as any)
    await hooks.event!({
      event: {
        type: "message.updated",
        properties: {
          sessionID: "ses_1",
          info: {
            id: "msg_late",
            role: "assistant",
            providerID: "moonshotai",
            modelID: "kimi-k3",
            time: { completed: 3 },
            tokens: { input: 1, output: 1, reasoning: 0, cache: { read: 0, write: 0 } },
          },
        },
      },
    } as any)
    await Bun.sleep(20)

    expect(payloads.map((item) => item.type)).toEqual(["message.part.updated", "message.updated"])
    expect(payloads[0].part.text).toBe("late role")
  })

  test("joins deltas when the completed part omits its session ID", async () => {
    const hooks = await plugin()
    await hooks.event!({
      event: {
        type: "message.updated",
        properties: {
          sessionID: "ses_1",
          info: { id: "msg_delta", role: "assistant", providerID: "moonshotai", modelID: "kimi-k3", time: {} },
        },
      },
    } as any)
    await hooks.event!({
      event: {
        type: "message.part.delta",
        properties: {
          sessionID: "ses_1",
          messageID: "msg_delta",
          partID: "prt_delta",
          field: "text",
          delta: "streamed text",
        },
      },
    } as any)
    await hooks.event!({
      event: {
        type: "message.part.updated",
        properties: {
          sessionID: "ses_1",
          part: {
            id: "prt_delta",
            messageID: "msg_delta",
            type: "text",
            text: "",
            time: { start: 1, end: 2 },
          },
        },
      },
    } as any)
    await Bun.sleep(20)

    expect(payloads).toHaveLength(1)
    expect(payloads[0].part.text).toBe("streamed text")
  })

  test("idle flush does not loop when a pending part still has no role", async () => {
    const hooks = await plugin()
    await hooks.event!({
      event: {
        type: "message.part.updated",
        properties: {
          sessionID: "ses_1",
          part: {
            id: "prt_unknown",
            messageID: "msg_unknown",
            sessionID: "ses_1",
            type: "text",
            text: "pending",
          },
        },
      },
    } as any)
    await hooks.event!({
      event: { type: "session.status", properties: { sessionID: "ses_1", status: { type: "idle" } } },
    } as any)
    await Bun.sleep(20)

    expect(payloads.map((item) => item.type)).toEqual(["session.status"])
  })

  test("cleans session state after deletion", async () => {
    const hooks = await plugin()
    const completed = (sessionID: string) => ({
      event: {
        type: "message.updated",
        properties: {
          sessionID,
          info: {
            id: "msg_reused",
            role: "assistant",
            providerID: "moonshotai",
            modelID: "kimi-k3",
            time: { completed: 2 },
            tokens: { input: 1, output: 1, reasoning: 0, cache: { read: 0, write: 0 } },
          },
        },
      },
    })
    await hooks.event!(completed("ses_1") as any)
    await hooks.event!({ event: { type: "session.deleted", properties: { sessionID: "ses_1" } } } as any)
    await Bun.sleep(20)
    await hooks.event!(completed("ses_2") as any)
    await Bun.sleep(20)

    expect(payloads.map((item) => item.type)).toEqual(["message.updated", "session.deleted", "message.updated"])
  })

  test("does not infer deletes from quoted rm text and preserves root model metadata", async () => {
    const hooks = await plugin()
    await hooks["tool.execute.before"]!(
      { tool: "bash", sessionID: "ses_1", callID: "call_echo" },
      { args: { command: "echo rm /tmp/not-deleted" } },
    )
    await hooks["tool.execute.after"]!(
      { tool: "bash", sessionID: "ses_1", callID: "call_echo", args: { command: "echo rm /tmp/not-deleted" } },
      { title: "echo", output: "rm /tmp/not-deleted", metadata: { exit: 0 } },
    )
    await hooks.event!({
      event: {
        type: "session.status",
        properties: {
          sessionID: "ses_1",
          status: { type: "busy" },
          model: { providerID: "moonshotai", modelID: "kimi-k3" },
        },
      },
    } as any)
    await Bun.sleep(20)

    expect(payloads[1].file_mutations).toEqual([])
    expect(payloads[2].model).toBe("moonshotai/kimi-k3")
  })

  test("requires a successful shell exit before emitting delete mutations", async () => {
    const hooks = await plugin()
    const path = "/tmp/maybe-not-deleted"
    await hooks["tool.execute.before"]!(
      { tool: "bash", sessionID: "ses_1", callID: "call_rm_unknown" },
      { args: { command: `rm "${path}"` } },
    )
    await hooks["tool.execute.after"]!(
      { tool: "bash", sessionID: "ses_1", callID: "call_rm_unknown", args: { command: `rm "${path}"` } },
      { title: "rm", output: "", metadata: {} },
    )
    await hooks.event!({
      event: {
        type: "session.diff",
        properties: {
          sessionID: "ses_1",
          diff: [{ file: path, before: "present", after: "", additions: 0, deletions: 1 }],
        },
      },
    } as any)
    await Bun.sleep(20)

    expect(payloads[1].file_mutations).toEqual([])
    expect(payloads[2].type).toBe("session.diff")
  })

  test("does not treat substrings inside custom tool names as file mutations", async () => {
    const hooks = await plugin()
    const path = "/repo/credit.txt"
    await hooks["tool.execute.before"]!(
      { tool: "credit_report", sessionID: "ses_1", callID: "call_credit" },
      { args: { filePath: path } },
    )
    await hooks.event!({
      event: {
        type: "session.diff",
        properties: {
          sessionID: "ses_1",
          diff: [{ file: path, before: "old", after: "new", additions: 1, deletions: 1 }],
        },
      },
    } as any)
    await Bun.sleep(20)

    expect(payloads.map((item) => item.type)).toEqual(["tool.execute.before", "session.diff"])
  })
})

// A minimal OpenCode 2.x promise-plugin context: hooks are captured so the test
// can fire them, and the event stream yields whatever the test pushes.
function v2Context() {
  const hooks = new Map<string, (input: any) => Promise<void> | void>()
  const queue: any[] = []
  let wake: (() => void) | undefined
  let closed = false
  const disposed: string[] = []
  const hook = (domain: string) => async (name: string, callback: any) => {
    hooks.set(`${domain}.${name}`, callback)
    return { dispose: async () => void disposed.push(`${domain}.${name}`) }
  }
  const ctx = {
    location: {
      directory: "/repo",
      project: { id: "project-1", directory: "/repo", canonical: "/repo" },
    },
    session: { hook: hook("session") },
    tool: { hook: hook("tool") },
    event: {
      subscribe: ({ signal }: { signal?: AbortSignal } = {}) => ({
        async *[Symbol.asyncIterator]() {
          signal?.addEventListener("abort", () => {
            closed = true
            wake?.()
          })
          while (!closed) {
            if (queue.length === 0) await new Promise<void>((resolve) => (wake = resolve))
            while (queue.length > 0) yield queue.shift()
          }
        },
      }),
    },
  }
  return {
    ctx,
    disposed,
    fire: async (name: string, input: any) => hooks.get(name)!(input),
    emit: (type: string, data: any, created = 5) => {
      queue.push({ id: `evt_${queue.length}`, type, created, data })
      wake?.()
    },
  }
}

describe("OpenCode 2.x plugin definition", () => {
  test("exports the default definition both loaders accept", () => {
    expect(BeaconDefinition.id).toBe("beacon.endpoint")
    expect(typeof BeaconDefinition.setup).toBe("function")
    expect(BeaconDefinition.server).toBe(BeaconEndpointPlugin)
  })

  test("maps V2 hooks and events onto the V1 payloads", async () => {
    const v2 = v2Context()
    const cleanup = await BeaconDefinition.setup(v2.ctx as any)

    v2.emit("session.created", {
      sessionID: "ses_1",
      projectID: "project-1",
      slug: "s",
      version: "2.0.15",
      model: { id: "kimi-k3", providerID: "moonshotai" },
    })
    await Bun.sleep(10)
    await v2.fire("session.prompt", {
      sessionID: "ses_1",
      messageID: "msg_user",
      prompt: { text: "summarize" },
      delivery: "immediate",
    })
    await v2.fire("tool.execute.before", {
      tool: "shell",
      sessionID: "ses_1",
      agent: "build",
      messageID: "msg_1",
      id: "call_1",
      input: { command: "git status --short" },
    })
    await v2.fire("tool.execute.after", {
      tool: "shell",
      sessionID: "ses_1",
      agent: "build",
      messageID: "msg_1",
      id: "call_1",
      input: { command: "git status --short" },
      status: "completed",
      result: {
        output: { output: " M a.txt", exit: 0, truncated: false },
        content: [{ type: "text", text: " M a.txt" }],
        metadata: { status: "completed" },
      },
    })
    await v2.fire("tool.execute.after", {
      tool: "read",
      sessionID: "ses_1",
      agent: "build",
      messageID: "msg_1",
      id: "call_2",
      input: { filePath: "/repo/missing" },
      status: "error",
      error: { _tag: "Tool.Error", message: "not found" },
    })
    v2.emit("session.execution.started", { sessionID: "ses_1" })
    v2.emit("session.status", { sessionID: "ses_1", status: { type: "busy" } })
    v2.emit("session.text.ended", { sessionID: "ses_1", assistantMessageID: "msg_1", ordinal: 0, text: "Done" })
    v2.emit("session.step.ended", {
      sessionID: "ses_1",
      assistantMessageID: "msg_1",
      finish: "stop",
      cost: 0.01,
      tokens: { input: 3, output: 4, reasoning: 1, cache: { read: 2, write: 0 } },
    })
    v2.emit("permission.asked", { id: "per_1", sessionID: "ses_1", action: "bash", resources: ["git push"] })
    v2.emit("permission.replied", { sessionID: "ses_1", requestID: "per_1", reply: "reject" })
    v2.emit("session.execution.succeeded", { sessionID: "ses_1" })
    v2.emit("session.idle", { sessionID: "ses_1" })
    await Bun.sleep(30)
    await (cleanup as any)()

    expect(payloads.map((item) => item.type)).toEqual([
      "session.created",
      "chat.message",
      "tool.execute.before",
      "tool.execute.after",
      "message.part.updated",
      "session.status",
      "message.part.updated",
      "message.updated",
      "permission.asked",
      "permission.replied",
      "session.status",
    ])
    expect(payloads[0]).toMatchObject({ session_id: "ses_1", directory: "/repo", worktree: "/repo" })
    expect(payloads[1]).toMatchObject({
      session_id: "ses_1",
      model: "moonshotai/kimi-k3",
      output: { parts: [{ type: "text", text: "summarize" }] },
    })
    expect(payloads[2]).toMatchObject({ call_id: "call_1", tool_name: "shell", tool_input: { command: "git status --short" } })
    expect(payloads[3]).toMatchObject({
      call_id: "call_1",
      tool_response: { output: " M a.txt", metadata: { exit: 0 } },
    })
    expect(payloads[4].part).toMatchObject({ type: "tool", callID: "call_2", state: { status: "error", error: "not found" } })
    expect(payloads[6]).toMatchObject({ model: "moonshotai/kimi-k3", part: { type: "text", text: "Done" } })
    expect(payloads[7].properties.info).toMatchObject({ tokens: { input: 3, output: 4 }, cost: 0.01 })
    expect(payloads[8].properties).toMatchObject({ permission: "bash", patterns: ["git push"] })
    expect(payloads[9].properties).toMatchObject({ requestID: "per_1", reply: "reject", permission: "bash" })
    expect(payloads[10].properties.status.type).toBe("idle")
    expect(v2.disposed.sort()).toEqual(["session.prompt", "tool.execute.after", "tool.execute.before"])
  })
})
