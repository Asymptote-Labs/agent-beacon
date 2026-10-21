// The one source of the serializer every Beacon runtime plugin runs on its hook payloads.
//
// Each plugin installs as a single self-contained file that its runtime loads directly, so a plugin
// cannot import this module at run time. Instead the region between the two markers below is
// written into each plugin by `bun run sync` (sync-safe-clone.ts), verbatim into the TypeScript
// plugins and with its two type annotations removed into OpenClaw's JavaScript one. `bun run check`
// in every plugin, and `bun test` here, fail when any copy differs from this file.
//
// Edit the region here, never in a plugin, then run `bun run sync` in this directory.

// BEGIN shared/safe-clone
// Generated from plugins/shared/safe-clone.ts by `bun run sync`. Edit that file, not this block:
// `bun run check` fails when the two differ.

// How deep into a payload the serializer will walk before giving up.
const maxDepth = 8

// safeClone turns a live runtime payload into something JSON.stringify accepts.
//
// Runtimes hand plugins live objects, not plain data: AbortSignals, SDK class instances, parent
// references, provider message objects, and tool inputs that are mutable by design. JSON.stringify
// throws on a circular reference and on a bigint, and silently flattens an Error to {}, so each is
// handled here rather than losing the whole payload to one bad field.
//
// Cycles are tracked along the current path and released on the way out, so an object that legibly
// appears twice -- the same file path referenced from two places in one edit -- is kept both times.
// A WeakSet that never released would drop the second occurrence as if it were a cycle.
function safeClone(value: unknown, depth = 0, seen = new WeakSet<object>()): unknown {
  if (value === null) return null
  const kind = typeof value
  if (kind === "function" || kind === "symbol" || kind === "undefined") return undefined
  if (kind === "bigint") return String(value)
  if (typeof value !== "object") return value
  if (depth >= maxDepth) return undefined

  if (seen.has(value)) return undefined
  seen.add(value)
  try {
    // Use native Date behavior without calling provider-overridden methods or toJSON.
    if (value instanceof Date) {
      return Number.isFinite(Date.prototype.getTime.call(value)) ? Date.prototype.toISOString.call(value) : null
    }
    if (Array.isArray(value)) {
      const length = Reflect.getOwnPropertyDescriptor(value, "length")?.value
      if (!Number.isInteger(length) || length < 0 || length > 0xffffffff) return undefined
      const out = new Array(length)
      // map also consults inherited slots and methods, and probes every sparse position.
      for (const key of Reflect.ownKeys(value)) {
        if (typeof key !== "string") continue
        const index = Number(key)
        if (!Number.isInteger(index) || index < 0 || index >= length || String(index) !== key) continue
        try {
          if (Reflect.getOwnPropertyDescriptor(value, key)?.enumerable) {
            out[index] = safeClone(Reflect.get(value, key), depth + 1, seen)
          }
        } catch {
          // An unreadable slot stays a hole without losing the rest of the array.
        }
      }
      return out
    }
    const out: Record<string, unknown> = {}
    if (value instanceof Error) {
      for (const key of ["name", "message"]) {
        try {
          let owner = value
          let descriptor
          for (let level = 0; owner && level <= maxDepth; level++) {
            descriptor = Reflect.getOwnPropertyDescriptor(owner, key)
            if (descriptor) break
            owner = Reflect.getPrototypeOf(owner)
          }
          // Preserve native inherited data names, never inherited accessor execution.
          if (!descriptor || (owner !== value && !("value" in descriptor))) continue
          const cloned = safeClone(Reflect.get(value, key), depth + 1, seen)
          if (cloned !== undefined) out[key] = cloned
        } catch {
          // Error overrides can expire just like other live runtime properties.
        }
      }
      return out
    }
    for (const key of Reflect.ownKeys(value)) {
      if (typeof key !== "string" || key === "__proto__") continue
      try {
        if (!Reflect.getOwnPropertyDescriptor(value, key)?.enumerable) continue
        const cloned = safeClone(Reflect.get(value, key), depth + 1, seen)
        if (cloned !== undefined) out[key] = cloned
      } catch {
        // A bad getter or proxy read costs its field, not the enclosing event.
      }
    }
    return out
  } catch {
    // Enumeration and revoked proxies can make a whole container inaccessible.
    return undefined
  } finally {
    seen.delete(value)
  }
}
// END shared/safe-clone

export { maxDepth, safeClone }
