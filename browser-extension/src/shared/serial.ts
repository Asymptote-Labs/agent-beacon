// serial returns a runner that executes the functions passed to it one at a time, in call order.
//
// The service worker is a single JS context, but two async read-modify-write cycles on the same
// chrome.storage key interleave at their awaits, and the later write replaces the earlier one with
// a stale snapshot. Each storage key with a read-modify-write gets its own runner; only this SW
// writes those keys, so an in-memory promise chain is enough. A rejected function rejects its own
// caller and does not stop the functions queued after it.
export function serial(): <T>(fn: () => Promise<T>) => Promise<T> {
  let tail: Promise<unknown> = Promise.resolve();
  return (fn) => {
    const result = tail.then(fn, fn);
    tail = result.then(
      () => undefined,
      () => undefined,
    );
    return result;
  };
}
