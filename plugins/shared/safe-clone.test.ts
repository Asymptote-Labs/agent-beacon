import { afterAll, describe, expect, test } from "bun:test"
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join, relative } from "node:path"

import { safeClone as canonicalSafeClone } from "./safe-clone.ts"
import {
  beginMarker,
  canonicalBlock,
  consumers,
  endMarker,
  expectedBlock,
  extractBlock,
  jsRewrites,
  pluginsDir,
  replaceBlock,
  repoRoot,
  syncFile,
  toJavaScript,
} from "./sync-safe-clone.ts"

type SafeClone = (value: unknown) => unknown

const assetsDir = join(repoRoot, "cli/beacon/internal/endpoint/hooks/assets")
const transpiler = new Bun.Transpiler({ loader: "ts" })

// load evaluates the serializer exactly as a plugin file carries it. The region is self-contained
// (it declares its own maxDepth), so it runs on its own; TypeScript regions are transpiled first,
// JavaScript ones are run as written, which also proves the JavaScript copy has no TypeScript left.
function load(path: string): SafeClone {
  const block = extractBlock(readFileSync(path, "utf8"), path)
  const code = path.endsWith(".js") ? block : transpiler.transformSync(block)
  return new Function(`${code}\nreturn safeClone`)() as SafeClone
}

// chain builds `layers` nested objects around a leaf: chain(2, "x") is {child: {child: "x"}}. The
// outermost object is at depth 0, so the innermost is at depth layers - 1.
function chain(layers: number, leaf: unknown): unknown {
  let value = leaf
  for (let i = 0; i < layers; i++) value = { child: value }
  return value
}

// Every distinct serializer form: the canonical module, one TypeScript plugin copy and the
// JavaScript copy, each evaluated from its plugin file. Every other copy is byte-identical to one
// of these, which the drift tests below enforce, so running the suite against it again adds nothing.
const typescriptCopy = consumers.find((c) => c.sourcePath.endsWith(".ts"))!
const javascriptCopy = consumers.find((c) => c.sourcePath.endsWith(".js"))!
const implementations: Array<[string, SafeClone]> = [
  ["plugins/shared/safe-clone.ts", canonicalSafeClone],
  ...[typescriptCopy.sourcePath, javascriptCopy.sourcePath].map(
    (path): [string, SafeClone] => [relative(repoRoot, path), load(path)],
  ),
]

describe.each(implementations)("%s", (_name, safeClone) => {
  describe("primitives", () => {
    test("pass through unchanged", () => {
      expect(safeClone("text")).toBe("text")
      expect(safeClone("")).toBe("")
      expect(safeClone(0)).toBe(0)
      expect(safeClone(-1.5)).toBe(-1.5)
      expect(safeClone(true)).toBe(true)
      expect(safeClone(false)).toBe(false)
    })

    test("leave non-finite numbers to JSON.stringify, which writes null", () => {
      expect(safeClone(Number.NaN)).toBeNaN()
      expect(safeClone(Number.POSITIVE_INFINITY)).toBe(Number.POSITIVE_INFINITY)
      expect(JSON.stringify(safeClone({ a: Number.NaN }))).toBe('{"a":null}')
    })

    test("keep null", () => {
      expect(safeClone(null)).toBeNull()
      expect(safeClone({ a: null })).toEqual({ a: null })
    })

    test("drop undefined, functions and symbols", () => {
      expect(safeClone(undefined)).toBeUndefined()
      expect(safeClone(() => 1)).toBeUndefined()
      expect(safeClone(class Thing {})).toBeUndefined()
      expect(safeClone(Symbol("s"))).toBeUndefined()
    })

    test("write a bigint as its decimal string instead of throwing", () => {
      expect(safeClone(12n)).toBe("12")
      expect(safeClone(-7n)).toBe("-7")
      expect(safeClone(2n ** 70n)).toBe("1180591620717411303424")
      expect(() => JSON.stringify({ n: 1n })).toThrow()
      expect(JSON.stringify(safeClone({ n: 1n }))).toBe('{"n":"1"}')
    })
  })

  describe("objects", () => {
    test("drop keys whose values do not survive", () => {
      const out = safeClone({ keep: 1, gone: undefined, fn() {}, sym: Symbol("s"), nul: null })
      expect(out).toEqual({ keep: 1, nul: null })
      expect(Object.keys(out as object)).toEqual(["keep", "nul"])
    })

    test("walk own enumerable string keys only", () => {
      const value: Record<string | symbol, unknown> = { visible: 1, [Symbol("key")]: 2 }
      Object.defineProperty(value, "hidden", { value: 3, enumerable: false })
      expect(safeClone(Object.create({ inherited: 4 }, { own: { value: 5, enumerable: true } }))).toEqual({ own: 5 })
      expect(safeClone(value)).toEqual({ visible: 1 })
    })

    test("flatten a class instance to its own fields, ignoring prototype getters and toJSON", () => {
      class Message {
        role = "user"
        #secret = "private"
        get upper() {
          return this.role.toUpperCase() + this.#secret
        }
        toJSON() {
          return "replaced"
        }
      }
      expect(safeClone(new Message())).toEqual({ role: "user" })
      expect(safeClone({ a: 1, toJSON: () => "replaced" })).toEqual({ a: 1 })
    })

    test("return a plain copy and leave the input untouched", () => {
      const input = { nested: { list: [1, { deep: true }] }, fn: () => 1 }
      const out = safeClone(input) as typeof input
      expect(out).toEqual({ nested: { list: [1, { deep: true }] } } as unknown as typeof input)
      expect(out).not.toBe(input)
      expect(out.nested).not.toBe(input.nested)
      expect(out.nested.list).not.toBe(input.nested.list)
      expect(typeof input.fn).toBe("function")
    })

    test("never write a __proto__ key onto Object.prototype", () => {
      const out = safeClone(JSON.parse('{"__proto__":{"polluted":true},"a":1}'))
      expect(({} as Record<string, unknown>).polluted).toBeUndefined()
      expect(JSON.stringify(out)).toBe('{"a":1}')
    })

    test("walk containers with no enumerable entries to an empty object, as JSON.stringify does", () => {
      expect(safeClone(new Map([["k", "v"]]))).toEqual({})
      expect(safeClone(new Set([1, 2]))).toEqual({})
      expect(safeClone(new WeakMap())).toEqual({})
      expect(safeClone(new AbortController().signal)).toEqual({})
      expect(JSON.stringify(new Map([["k", "v"]]))).toBe("{}")
    })

    test("write a typed array by its indices", () => {
      expect(safeClone(new Uint8Array([7, 8]))).toEqual({ 0: 7, 1: 8 })
    })
  })

  describe("arrays", () => {
    test("keep every position, leaving dropped items as undefined for JSON.stringify to null", () => {
      const out = safeClone([1, undefined, () => 1, Symbol("s"), "x"])
      expect(out).toEqual([1, undefined, undefined, undefined, "x"])
      expect((out as unknown[]).length).toBe(5)
      expect(JSON.stringify(out)).toBe('[1,null,null,null,"x"]')
    })

    test("keep holes as holes", () => {
      // eslint-disable-next-line no-sparse-arrays
      const out = safeClone([, 1]) as unknown[]
      expect(out.length).toBe(2)
      expect(0 in out).toBe(false)
      expect(JSON.stringify(out)).toBe("[null,1]")
    })
  })

  describe("Error", () => {
    test("keep only name and message, which JSON.stringify would lose", () => {
      const err = Object.assign(new Error("boom", { cause: new Error("root") }), { code: "E_X" })
      expect(JSON.stringify(err)).toBe('{"code":"E_X"}')
      expect(safeClone(err)).toEqual({ name: "Error", message: "boom" })
    })

    test("keep the subclass name", () => {
      expect(safeClone(new TypeError("bad"))).toEqual({ name: "TypeError", message: "bad" })
      class ToolFailure extends Error {
        override name = "ToolFailure"
      }
      expect(safeClone({ error: new ToolFailure("denied") })).toEqual({
        error: { name: "ToolFailure", message: "denied" },
      })
    })
  })

  describe("Date", () => {
    test("write a valid Date as its ISO string", () => {
      expect(safeClone(new Date(0))).toBe("1970-01-01T00:00:00.000Z")
      expect(safeClone({ at: new Date("2026-10-14T12:34:56.789Z") })).toEqual({ at: "2026-10-14T12:34:56.789Z" })
    })

    test("write an invalid Date as null, as JSON.stringify does, instead of throwing", () => {
      const invalid = new Date(Number.NaN)
      expect(() => invalid.toISOString()).toThrow()
      expect(JSON.stringify({ at: invalid })).toBe('{"at":null}')
      expect(safeClone(invalid)).toBeNull()
      expect(safeClone({ at: invalid, keep: 1 })).toEqual({ at: null, keep: 1 })
      expect(safeClone([invalid])).toEqual([null])
    })
  })

  describe("cycles", () => {
    test("drop a key that points back at an ancestor", () => {
      const value: Record<string, unknown> = { id: "root" }
      value.self = value
      value.child = { parent: value, name: "child" }
      expect(() => JSON.stringify(value)).toThrow()
      expect(safeClone(value)).toEqual({ id: "root", child: { name: "child" } })
    })

    test("leave a cyclic array item undefined", () => {
      const list: unknown[] = [1]
      list.push(list)
      expect(safeClone(list)).toEqual([1, undefined])
      expect(JSON.stringify(safeClone(list))).toBe("[1,null]")
    })

    test("break a cycle through an Error's owner", () => {
      const owner: Record<string, unknown> = { error: new Error("x") }
      owner.loop = { back: owner }
      expect(safeClone(owner)).toEqual({ error: { name: "Error", message: "x" }, loop: {} })
    })

    test("keep an object that appears twice without a cycle", () => {
      const path = { file: "/repo/a.ts" }
      const out = safeClone({ before: path, after: path, list: [path, path] }) as Record<string, unknown>
      expect(out).toEqual({ before: path, after: path, list: [path, path] })
      expect(out.before).not.toBe(out.after)
    })
  })

  describe("depth limit", () => {
    test("keep objects down to depth seven and a primitive at depth eight", () => {
      const deepest = chain(8, "leaf")
      expect(safeClone(deepest)).toEqual(deepest)
    })

    test("drop an object at depth eight, leaving its parent empty", () => {
      expect(safeClone(chain(9, "leaf"))).toEqual(chain(7, {}))
      expect(safeClone(chain(40, "leaf"))).toEqual(chain(7, {}))
    })

    test("count array nesting the same way", () => {
      let kept: unknown = "leaf"
      for (let i = 0; i < 8; i++) kept = [kept]
      expect(safeClone(kept)).toEqual(kept)
      expect(safeClone([kept])).toEqual([[[[[[[[undefined]]]]]]]])
    })

    test("drop an Error or a Date past the limit like any other object", () => {
      expect(safeClone(chain(8, new Error("deep")))).toEqual(chain(7, {}))
      expect(safeClone(chain(8, new Date(0)))).toEqual(chain(7, {}))
      expect(safeClone(chain(7, new Error("ok")))).toEqual(chain(7, { name: "Error", message: "ok" }))
    })

    test("measure depth from the root along each branch", () => {
      const out = safeClone({ deep: chain(9, "x"), shallow: { a: 1 } })
      expect(out).toEqual({ deep: chain(6, {}), shallow: { a: 1 } })
    })
  })

  describe("size", () => {
    test("pass a wide or long payload through whole; size caps live in beacon-hooks", () => {
      const wide = Object.fromEntries(Array.from({ length: 5000 }, (_, i) => [`k${i}`, i]))
      const long = "x".repeat(1 << 20)
      const out = safeClone({ wide, long, list: Array.from({ length: 5000 }, (_, i) => i) }) as {
        wide: object
        long: string
        list: unknown[]
      }
      expect(Object.keys(out.wide).length).toBe(5000)
      expect(out.long.length).toBe(1 << 20)
      expect(out.list.length).toBe(5000)
    })
  })

  test("always produce something JSON.stringify accepts", () => {
    const hostile: Record<string, unknown> = {
      big: 9007199254740993n,
      err: new RangeError("r"),
      when: new Date(Number.NaN),
      fn: () => {},
      signal: new AbortController().signal,
      map: new Map([[1, 2]]),
      deep: chain(30, 1n),
    }
    hostile.cycle = hostile
    hostile.list = [hostile, 1n, new Error("e"), undefined]
    const json = JSON.stringify(safeClone(hostile))
    expect(JSON.parse(json)).toEqual({
      big: "9007199254740993",
      err: { name: "RangeError", message: "r" },
      when: null,
      signal: {},
      map: {},
      deep: chain(6, {}),
      list: [null, "1", { name: "Error", message: "e" }, null],
    })
  })
})

describe("every plugin runs the shared serializer", () => {
  const canonical = canonicalBlock()

  test.each(consumers.map((c) => [c.plugin, c] as const))("%s source and embedded copy match", (_, c) => {
    for (const path of [c.sourcePath, c.embeddedPath]) {
      expect({ path: relative(repoRoot, path), block: extractBlock(readFileSync(path, "utf8"), path) }).toEqual({
        path: relative(repoRoot, path),
        block: expectedBlock(canonical, path),
      })
    }
  })

  // A copy written outside the markers would escape the drift check, so look for one everywhere a
  // plugin lives: every plugin source and every embedded asset.
  test("no plugin carries a serializer or depth limit of its own outside the shared block", () => {
    const roots = [
      ...readdirSync(pluginsDir).filter((dir) => dir !== "shared").map((dir) => join(pluginsDir, dir, "src")),
      assetsDir,
    ]
    const sources = new Map(
      roots
        .filter((root) => existsSync(root))
        .flatMap((root) => readdirSync(root, { recursive: true, encoding: "utf8" }).map((name) => join(root, name)))
        .filter((path) => /\.[cm]?[jt]s$/.test(path) && !/\.test\.[jt]s$/.test(path))
        .map((path) => [relative(repoRoot, path), readFileSync(path, "utf8")] as const)
        .filter(([, source]) => /function safeClone\b/.test(source)),
    )
    const expected = consumers.flatMap((c) => [c.sourcePath, c.embeddedPath]).map((p) => relative(repoRoot, p))
    expect([...sources.keys()].sort()).toEqual(expected.sort())

    for (const [path, source] of sources) {
      const outside = source.replace(extractBlock(source, path), "")
      expect({ path, outside: /function safeClone\b|\bmaxDepth\s*=/.test(outside) }).toEqual({ path, outside: false })
    }
  })
})

describe("sync-safe-clone", () => {
  const canonical = canonicalBlock()

  test("the JavaScript copy differs from the TypeScript one only by the listed rewrites", () => {
    const ts = canonical.split("\n")
    const js = toJavaScript(canonical).split("\n")
    expect(js.length).toBe(ts.length)
    const changed = ts.flatMap((line, i) => (line === js[i] ? [] : [[line, js[i]]]))
    expect(changed).toEqual(jsRewrites.map(([from, to]) => [from, to]))
    expect(expectedBlock(canonical, "x/beacon.ts")).toBe(canonical)
    expect(expectedBlock(canonical, "x/beacon.js")).toBe(toJavaScript(canonical))
  })

  test("refuses to emit JavaScript when a rewrite no longer matches exactly one line", () => {
    const [signature] = jsRewrites[0]
    expect(() => toJavaScript(canonical.replace(signature, `${signature} `))).toThrow(/found 0/)
    expect(() => toJavaScript(`${canonical}\n${signature}`)).toThrow(/found 2/)
  })

  test("the generated JavaScript parses as JavaScript", () => {
    expect(() => new Function(toJavaScript(canonical))).not.toThrow()
    expect(() => new Function(canonical)).toThrow(SyntaxError)
  })

  test("extractBlock refuses missing, repeated or reversed markers", () => {
    const block = `${beginMarker}\nx\n${endMarker}`
    expect(extractBlock(`a\n${block}\nb`)).toBe(block)
    expect(() => extractBlock("no markers")).toThrow(/no "/)
    expect(() => extractBlock(`${beginMarker}\nx`)).toThrow(/no "/)
    expect(() => extractBlock(`${block}\n${block}`)).toThrow(/more than once/)
    expect(() => extractBlock(`${endMarker}\nx\n${beginMarker}`)).toThrow(/comes before/)
  })

  test("replaceBlock rewrites only the region and is idempotent", () => {
    const before = `head\r\n  ${beginMarker}\nold\n${endMarker} tail\n`
    const replaced = replaceBlock(before, `${beginMarker}\nnew\n${endMarker}`)
    expect(replaced).toBe(`head\r\n  ${beginMarker}\nnew\n${endMarker} tail\n`)
    expect(replaceBlock(replaced, `${beginMarker}\nnew\n${endMarker}`)).toBe(replaced)
  })

  describe("on a plugin file", () => {
    const dir = mkdtempSync(join(tmpdir(), "safe-clone-sync-"))
    const script = join(import.meta.dir, "sync-safe-clone.ts")
    const run = (...args: string[]) => Bun.spawnSync(["bun", script, ...args], { stderr: "pipe", stdout: "pipe" })
    const tampered = (name: string) => {
      const path = join(dir, name)
      const block = expectedBlock(canonical, path)
      writeFileSync(path, `before\n${block.replace("const maxDepth = 8", "const maxDepth = 9")}\nafter\n`)
      return path
    }

    test("check reports drift without writing, and sync repairs it", () => {
      for (const name of ["plugin.ts", "plugin.js"]) {
        const path = tampered(name)
        const original = readFileSync(path, "utf8")
        expect(syncFile(path, canonical, true)).toContain("differs from plugins/shared/safe-clone.ts")
        expect(readFileSync(path, "utf8")).toBe(original)
        expect(syncFile(path, canonical, false)).toBeUndefined()
        expect(readFileSync(path, "utf8")).toBe(`before\n${expectedBlock(canonical, path)}\nafter\n`)
        expect(syncFile(path, canonical, true)).toBeUndefined()
      }
    })

    test("the command exits non-zero on drift and zero once synced", () => {
      const path = tampered("cli.ts")
      const failed = run("--check", path)
      expect(failed.exitCode).toBe(1)
      expect(failed.stderr.toString()).toContain("differs from plugins/shared/safe-clone.ts")
      expect(run(path).exitCode).toBe(0)
      expect(run("--check", path).exitCode).toBe(0)
    })

    test("the command fails on a file with no region rather than skipping it", () => {
      const path = join(dir, "bare.ts")
      writeFileSync(path, "function safeClone() {}\n")
      const failed = run("--check", path)
      expect(failed.exitCode).toBe(1)
      expect(failed.stderr.toString()).toContain("no \"// BEGIN shared/safe-clone\"")
      expect(run(path).exitCode).toBe(1)
      expect(readFileSync(path, "utf8")).toBe("function safeClone() {}\n")
    })

    afterAll(() => rmSync(dir, { recursive: true, force: true }))
  })
})
