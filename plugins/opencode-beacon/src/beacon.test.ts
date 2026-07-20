import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import { BeaconEndpointPlugin } from "./beacon"

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
      "chat.message",
      "tool.execute.before",
      "tool.execute.after",
    ])
    expect(payloads[2]).toMatchObject({
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
})
