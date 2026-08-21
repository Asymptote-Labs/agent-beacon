import { afterEach, describe, expect, test } from "bun:test"

import { createBeaconPlugin } from "./beacon"

const senderKey = Symbol.for("beacon.cline.testSender")

type Sent = Record<string, unknown>

function captureSends(): Sent[] {
  const sent: Sent[] = []
  ;(globalThis as Record<symbol, unknown>)[senderKey] = (payload: Sent) => {
    sent.push(payload)
  }
  return sent
}

afterEach(() => {
  delete (globalThis as Record<symbol, unknown>)[senderKey]
})

describe("beacon cline plugin", () => {
  test("declares itself as a hooks plugin", () => {
    const plugin = createBeaconPlugin()
    expect(plugin.manifest.capabilities).toEqual(["hooks"])
    expect(Object.keys(plugin.hooks).sort()).toEqual(["afterRun", "afterTool", "beforeRun", "beforeTool"])
  })

  test("forwards each hook with its stage name", async () => {
    const sent = captureSends()
    const plugin = createBeaconPlugin()

    await plugin.hooks.beforeRun({ taskId: "task-1" })
    await plugin.hooks.beforeTool({ taskId: "task-1", toolCall: { name: "read_file" } })
    await plugin.hooks.afterTool({ taskId: "task-1", toolCall: { name: "read_file" } })
    await plugin.hooks.afterRun({ taskId: "task-1" })

    expect(sent.map((event) => event.type)).toEqual(["beforeRun", "beforeTool", "afterTool", "afterRun"])
  })

  // The hook adapter reads these at the top level of the envelope. Leaving them wherever the
  // context happened to nest them would cost the session id, which is what groups a task's events.
  test("lifts task identity out of nested context shapes", async () => {
    const sent = captureSends()
    const plugin = createBeaconPlugin()

    await plugin.hooks.afterTool({ task: { id: "task-nested" }, workspace_roots: ["/repo"] })

    expect(sent[0].taskId).toBe("task-nested")
    expect(sent[0].workspaceRoots).toEqual(["/repo"])
  })

  // The setup context is the only place the workspace identity is guaranteed to appear, so it has
  // to reach hooks that did not carry it themselves.
  test("carries setup context onto later hooks", async () => {
    const sent = captureSends()
    const plugin = createBeaconPlugin()

    plugin.setup(undefined, { workspaceRoots: ["/repo"], clineVersion: "3.36.0" })
    await plugin.hooks.beforeTool({ toolCall: { name: "read_file" } })

    expect(sent[0].workspaceRoots).toEqual(["/repo"])
    expect(sent[0].clineVersion).toBe("3.36.0")
  })

  test("context fields win over the captured setup context", async () => {
    const sent = captureSends()
    const plugin = createBeaconPlugin()

    plugin.setup(undefined, { taskId: "task-setup" })
    await plugin.hooks.afterRun({ taskId: "task-current" })

    expect(sent[0].taskId).toBe("task-current")
  })

  // Cline awaits these handlers inside its own run loop. A hook that returns a rejected promise, or
  // that throws, is a hook that can break the run it was only supposed to observe.
  test("never rejects when the sender throws", async () => {
    ;(globalThis as Record<symbol, unknown>)[senderKey] = () => {
      throw new Error("sender exploded")
    }
    const plugin = createBeaconPlugin()

    await expect(plugin.hooks.beforeTool({ taskId: "task-1" })).resolves.toBeUndefined()
  })

  test("observes without cancelling: every hook resolves to undefined", async () => {
    captureSends()
    const plugin = createBeaconPlugin()

    for (const hook of Object.values(plugin.hooks)) {
      expect(await hook({ taskId: "task-1", toolCall: { name: "execute_command" } })).toBeUndefined()
    }
  })

  // A live SDK context is not plain data. JSON.stringify throws on a cycle, so a payload carrying
  // one has to survive as the fields around it rather than being dropped whole.
  test("survives a circular context", async () => {
    const sent = captureSends()
    const plugin = createBeaconPlugin()
    const context: Record<string, unknown> = { taskId: "task-cycle" }
    context.self = context

    await plugin.hooks.afterRun(context)

    expect(sent[0].taskId).toBe("task-cycle")
    expect(() => JSON.stringify(sent[0])).not.toThrow()
  })
})
