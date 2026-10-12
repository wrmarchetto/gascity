import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type * as CityEventStreamModule from './cityEventStream';

// Scope: the page-side adapter's claim that it presents the SAME shape
// EventSource does, so useGcEvents.ts's connect/grace/backoff/degraded policy
// works over the shared worker unchanged.
//
// Why this exists alongside e2e/connection-budget.spec.ts: that spec proves
// six open pages stop starving each other, but every assertion it makes is
// satisfied by a worker that forwards only `open`. The lamps it reads come
// from a fetch, not from a gc event, so an adapter that dropped every
// forwarded event -- leaving the dashboard permanently stale -- would pass
// it. These tests drive the hub->page direction directly.
//
// It does NOT model a real SharedWorker: jsdom supplies none, so the worker
// module is replaced and the port is a stand-in. The stand-in records what it
// is sent and delivers only what a test scripts; it answers nothing on its
// own, so a message the suite forgot to script cannot read as a pass.
//
// Run: npx vitest run src/events   (from this frontend workspace)

const STREAM_URL = 'http://127.0.0.1/v0/city/a/events/stream';

class FakePort {
  readonly sent: unknown[] = [];
  onmessage: ((event: MessageEvent<unknown>) => void) | null = null;
  started = false;
  closed = false;

  postMessage(message: unknown): void {
    this.sent.push(message);
  }

  start(): void {
    this.started = true;
  }

  close(): void {
    this.closed = true;
  }

  /** Deliver one hub->page message. */
  deliver(payload: unknown): void {
    this.onmessage?.({ data: payload } as MessageEvent<unknown>);
  }
}

class FakeWorker {
  static instances: FakeWorker[] = [];
  static constructionThrows = false;
  readonly port = new FakePort();
  onerror: (() => void) | null = null;

  constructor() {
    if (FakeWorker.constructionThrows) throw new Error('worker refused');
    FakeWorker.instances.push(this);
  }
}

class FakeEventSource {
  static readonly instances: FakeEventSource[] = [];
  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }
  close(): void {}
}

vi.mock('./cityEventStream.sharedworker?sharedworker', () => ({ default: FakeWorker }));

/**
 * load imports the adapter FRESH. It latches a failed worker in module scope
 * so the fallback is not re-attempted on every reconnect, and that latch
 * would otherwise carry one test's failure into the next.
 */
async function load(): Promise<typeof CityEventStreamModule> {
  vi.resetModules();
  return import('./cityEventStream');
}

describe('openCityEventStream', () => {
  beforeEach(() => {
    FakeWorker.instances = [];
    FakeWorker.constructionThrows = false;
    FakeEventSource.instances.length = 0;
    vi.stubGlobal('SharedWorker', FakeWorker);
    vi.stubGlobal('EventSource', FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('subscribes through the worker instead of opening its own EventSource', async () => {
    const { openCityEventStream } = await load();

    openCityEventStream(STREAM_URL);

    expect(FakeWorker.instances[0]?.port.sent).toEqual([{ t: 'subscribe', url: STREAM_URL }]);
    expect(FakeWorker.instances[0]?.port.started).toBe(true);
    // The whole point: no socket of this page's own.
    expect(FakeEventSource.instances).toHaveLength(0);
  });

  it('delivers a forwarded gc event to onmessage AND the named listener', async () => {
    const { openCityEventStream } = await load();
    const stream = openCityEventStream(STREAM_URL)!;
    const viaOnMessage = vi.fn();
    const viaNamed = vi.fn();
    stream.onmessage = viaOnMessage;
    stream.addEventListener('event', viaNamed);

    FakeWorker.instances[0]?.port.deliver({ t: 'event', data: '{"type":"bead.updated"}' });

    // Both, because gc names its events `event` and EventSource routes a
    // named event ONLY to addEventListener -- while useGcEvents.ts registers
    // both handlers against the same dispatch. An adapter that served one
    // would work against a server that named its events the other way and
    // fail silently against this one.
    expect(viaOnMessage).toHaveBeenCalledTimes(1);
    expect(viaNamed).toHaveBeenCalledTimes(1);
    expect((viaOnMessage.mock.calls[0]?.[0] as MessageEvent<string>).data).toBe(
      '{"type":"bead.updated"}',
    );
    expect((viaNamed.mock.calls[0]?.[0] as MessageEvent<string>).data).toBe(
      '{"type":"bead.updated"}',
    );
  });

  it('reports open with EventSource.OPEN readyState so the grace timer agrees', async () => {
    const { openCityEventStream } = await load();
    const stream = openCityEventStream(STREAM_URL)!;
    const onopen = vi.fn();
    stream.onopen = onopen;
    expect(stream.readyState).toBe(0);

    FakeWorker.instances[0]?.port.deliver({ t: 'open' });

    expect(onopen).toHaveBeenCalledTimes(1);
    expect(stream.readyState).toBe(1);
  });

  it('leaves readyState CONNECTING on a hub error so the subscriber owns the verdict', async () => {
    const { openCityEventStream } = await load();
    const stream = openCityEventStream(STREAM_URL)!;
    const onerror = vi.fn();
    stream.onerror = onerror;
    FakeWorker.instances[0]?.port.deliver({ t: 'open' });

    FakeWorker.instances[0]?.port.deliver({ t: 'error' });

    expect(onerror).toHaveBeenCalledTimes(1);
    // NOT 2 (CLOSED): useGcEvents.ts's grace timer reads CLOSED as "this
    // stream is finished" and would stop promoting the state to open after a
    // transient error the hub is about to recover from.
    expect(stream.readyState).toBe(0);
  });

  it('unsubscribes and stops delivering on close', async () => {
    const { openCityEventStream } = await load();
    const stream = openCityEventStream(STREAM_URL)!;
    const onmessage = vi.fn();
    stream.onmessage = onmessage;
    const port = FakeWorker.instances[0]!.port;

    stream.close();

    expect(port.sent).toContainEqual({ t: 'unsubscribe', url: STREAM_URL });
    expect(port.closed).toBe(true);
    // A message racing the unsubscribe must not reach a torn-down consumer:
    // useGcEvents.ts closes on unmount, and a late onMatch there refetches
    // into an unmounted component.
    port.deliver({ t: 'event', data: '{"type":"bead.updated"}' });
    expect(onmessage).not.toHaveBeenCalled();
  });

  it('falls back to this page owning the socket when there is no SharedWorker', async () => {
    vi.stubGlobal('SharedWorker', undefined);
    const { openCityEventStream } = await load();

    const stream = openCityEventStream(STREAM_URL);

    expect(stream).not.toBeNull();
    expect(FakeEventSource.instances.map((source) => source.url)).toEqual([STREAM_URL]);
  });

  it('latches the fallback after the worker fails rather than retrying it per reconnect', async () => {
    const { openCityEventStream } = await load();
    const stream = openCityEventStream(STREAM_URL)!;
    const onerror = vi.fn();
    stream.onerror = onerror;

    // How an unsupported worker script type actually surfaces: not a throw
    // from the constructor, but an error event on an object that then never
    // delivers anything. Without this the page would simply stop refreshing,
    // with no state change to show for it.
    FakeWorker.instances[0]?.onerror?.();
    expect(onerror).toHaveBeenCalledTimes(1);

    openCityEventStream(STREAM_URL);
    expect(FakeEventSource.instances.map((source) => source.url)).toEqual([STREAM_URL]);
  });

  it('latches the fallback when the worker constructor throws', async () => {
    const { openCityEventStream } = await load();
    FakeWorker.constructionThrows = true;

    expect(openCityEventStream(STREAM_URL)).not.toBeNull();

    FakeWorker.constructionThrows = false;
    openCityEventStream(STREAM_URL);
    // Still direct: one refusal is enough, and re-attempting it would pay a
    // thrown construction on every one of useGcEvents.ts's backoff retries.
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(FakeWorker.instances).toHaveLength(0);
  });

  it('reports no transport at all rather than a stream that never opens', async () => {
    vi.stubGlobal('SharedWorker', undefined);
    vi.stubGlobal('EventSource', undefined);
    const { openCityEventStream } = await load();

    // null, not a CLOSED-readyState stub: useGcEvents.ts reports `closed`
    // off this, and a stub would instead sit in `connecting` forever.
    expect(openCityEventStream(STREAM_URL)).toBeNull();
  });
});
