// The private interceptor -> content-script channel (src/shared/channel.ts).
// A plain EventTarget stands in for the page's window: what matters is the
// event protocol, which is the same object model in both worlds.
import { describe, it, expect, vi } from 'vitest';
import { acceptChannel, offerChannel, HELLO_EVENT, PORT_EVENT, type ChannelHost } from '../../src/shared/channel.js';

function nextMessage(port: MessagePort): Promise<unknown> {
  return new Promise((resolve) => {
    port.onmessage = (e) => resolve(e.data);
  });
}

function closeAll(...ports: (MessagePort | undefined)[]): void {
  for (const p of ports) p?.close();
}

describe('private channel hand-over', () => {
  it('connects when the interceptor runs first (manifest order)', async () => {
    const host: ChannelHost = new EventTarget();
    const mine = offerChannel(host);
    let theirs: MessagePort | undefined;
    acceptChannel(host, (p) => (theirs = p));
    expect(theirs).toBeDefined();

    const got = nextMessage(theirs!);
    mine.postMessage('{"kind":"done","reqId":"abcdefgh"}');
    expect(await got).toBe('{"kind":"done","reqId":"abcdefgh"}');
    closeAll(mine, theirs);
  });

  it('connects when the content script runs first', async () => {
    const host: ChannelHost = new EventTarget();
    let theirs: MessagePort | undefined;
    acceptChannel(host, (p) => (theirs = p));
    expect(theirs).toBeUndefined();
    const mine = offerChannel(host);
    expect(theirs).toBeDefined();

    const got = nextMessage(theirs!);
    mine.postMessage('x');
    expect(await got).toBe('x');
    closeAll(mine, theirs);
  });

  it('ignores a port a page script offers after the hand-over', () => {
    const host: ChannelHost = new EventTarget();
    const mine = offerChannel(host);
    const onPort = vi.fn();
    acceptChannel(host, onPort);
    expect(onPort).toHaveBeenCalledTimes(1);

    const forged = new MessageChannel();
    const claimed = !host.dispatchEvent(
      new MessageEvent(PORT_EVENT, { ports: [forged.port2], cancelable: true }),
    );
    expect(claimed).toBe(false);
    expect(onPort).toHaveBeenCalledTimes(1);
    closeAll(mine, forged.port1, forged.port2, onPort.mock.calls[0][0]);
  });

  it('does not hand the port out again to a later hello', () => {
    const host: ChannelHost = new EventTarget();
    const mine = offerChannel(host);
    let theirs: MessagePort | undefined;
    acceptChannel(host, (p) => (theirs = p));

    // A page script listening for the port and asking for it again gets nothing.
    const thief = vi.fn();
    host.addEventListener(PORT_EVENT, thief);
    host.dispatchEvent(new Event(HELLO_EVENT));
    expect(thief).not.toHaveBeenCalled();
    closeAll(mine, theirs);
  });

  it('refuses a port once the document has finished loading', () => {
    const doc = { readyState: 'loading', addEventListener: vi.fn() };
    const host = Object.assign(new EventTarget(), { document: doc }) as ChannelHost;
    const onPort = vi.fn();
    acceptChannel(host, onPort);
    doc.readyState = 'interactive';

    const late = new MessageChannel();
    host.dispatchEvent(new MessageEvent(PORT_EVENT, { ports: [late.port2], cancelable: true }));
    expect(onPort).not.toHaveBeenCalled();
    closeAll(late.port1, late.port2);
  });
});
