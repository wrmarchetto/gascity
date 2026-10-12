import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Scope: the shared-worker hub's ONE invariant -- at most one EventSource per
// stream URL for the whole browser profile -- plus the fan-out and teardown
// that invariant depends on.
//
// Why this exists alongside e2e/connection-budget.spec.ts: that spec proves
// the socket budget survives six open pages, which is the user-visible claim,
// but it costs a built bundle, a seeded server and six real page loads, and
// it can only observe the hub's effect from outside. A hub that forwarded
// `open` and silently dropped every `event` would pass it -- the lamps the
// spec reads come from a fetch, not from a gc event. These tests drive the
// hub's port protocol directly, so dedupe, fan-out and last-subscriber
// teardown each fail on their own.
//
// It does NOT model a real SharedWorker: jsdom supplies neither
// SharedWorkerGlobalScope nor MessagePort, so `onconnect` is read off the
// jsdom global the module assigns it to and the ports are stand-ins. What
// that cannot represent -- structured-clone of the payload, port lifetime
// across a page crash, and the browser's one-instance-per-profile guarantee
// itself -- is exactly what connection-budget.spec.ts covers in a real
// browser.
//
// Run: npx vitest run src/events   (from this frontend workspace)

const URL_A = 'http://127.0.0.1/v0/city/a/events/stream';
const URL_B = 'http://127.0.0.1/v0/city/b/events/stream';

/** The EventSource surface the hub touches, with its constructions recorded. */
class FakeEventSource {
  static readonly instances: FakeEventSource[] = [];
  readonly OPEN = 1;
  readyState = 0;
  closed = false;
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  onerror: (() => void) | null = null;
  private named: EventListener[] = [];

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: EventListener): void {
    if (type === 'event') this.named.push(listener);
  }

  close(): void {
    this.closed = true;
    this.readyState = 2;
  }

  /** Drive the open handshake the way the server's first byte would. */
  open(): void {
    this.readyState = this.OPEN;
    this.onopen?.();
  }

  /** Deliver one named gc event, which is the only shape gc actually sends. */
  emit(data: string): void {
    for (const listener of this.named) listener({ data } as unknown as Event);
  }
}

/** The MessagePort surface the hub touches, with its sends recorded. */
class FakePort {
  readonly sent: unknown[] = [];
  onmessage: ((event: MessageEvent<unknown>) => void) | null = null;
  started = false;

  postMessage(message: unknown): void {
    this.sent.push(message);
  }

  start(): void {
    this.started = true;
  }

  /** Deliver one page->hub message. */
  send(payload: unknown): void {
    this.onmessage?.({ data: payload } as MessageEvent<unknown>);
  }
}

type ConnectScope = { onconnect: ((event: MessageEvent) => void) | null };

/**
 * loadHub imports the hub FRESH and returns its `onconnect`. The module holds
 * its stream table in module scope and installs the handler as an import side
 * effect, so a shared import would carry one test's open streams into the
 * next and the dedupe assertions would read whatever ran before them.
 */
async function loadHub(): Promise<(event: MessageEvent) => void> {
  vi.resetModules();
  await import('./cityEventStream.sharedworker');
  const handler = (globalThis as unknown as ConnectScope).onconnect;
  if (handler === null || handler === undefined) throw new Error('hub installed no onconnect');
  return handler;
}

/** connect drives one page's attachment and returns its port stand-in. */
function connect(hub: (event: MessageEvent) => void): FakePort {
  const port = new FakePort();
  hub({ ports: [port] } as unknown as MessageEvent);
  return port;
}

describe('city event stream hub', () => {
  beforeEach(() => {
    FakeEventSource.instances.length = 0;
    vi.stubGlobal('EventSource', FakeEventSource);
    (globalThis as unknown as ConnectScope).onconnect = null;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('opens exactly one EventSource however many ports subscribe to one url', async () => {
    const hub = await loadHub();

    const first = connect(hub);
    const second = connect(hub);
    const third = connect(hub);
    first.send({ t: 'subscribe', url: URL_A });
    second.send({ t: 'subscribe', url: URL_A });
    third.send({ t: 'subscribe', url: URL_A });

    expect(FakeEventSource.instances).toHaveLength(1);
    expect(FakeEventSource.instances[0]?.url).toBe(URL_A);
  });

  it('keeps distinct urls on distinct streams', async () => {
    const hub = await loadHub();

    connect(hub).send({ t: 'subscribe', url: URL_A });
    connect(hub).send({ t: 'subscribe', url: URL_B });

    expect(FakeEventSource.instances.map((source) => source.url)).toEqual([URL_A, URL_B]);
  });

  it('fans one named gc event out to every subscribed port', async () => {
    const hub = await loadHub();
    const first = connect(hub);
    const second = connect(hub);
    first.send({ t: 'subscribe', url: URL_A });
    second.send({ t: 'subscribe', url: URL_A });

    FakeEventSource.instances[0]?.emit('{"type":"bead.updated"}');

    const expected = { t: 'event', data: '{"type":"bead.updated"}' };
    expect(first.sent).toContainEqual(expected);
    expect(second.sent).toContainEqual(expected);
  });

  it('tells a port that joins an already-open stream it is open', async () => {
    const hub = await loadHub();
    const first = connect(hub);
    first.send({ t: 'subscribe', url: URL_A });
    FakeEventSource.instances[0]?.open();

    // A late joiner gets no `open` from the source -- that fired before it
    // existed -- so without this the page would sit in `connecting` until the
    // next gc event happened to arrive, which on a quiet city is never.
    const late = connect(hub);
    late.send({ t: 'subscribe', url: URL_A });

    expect(late.sent).toContainEqual({ t: 'open' });
  });

  it('holds the socket until the LAST subscriber leaves', async () => {
    const hub = await loadHub();
    const first = connect(hub);
    const second = connect(hub);
    first.send({ t: 'subscribe', url: URL_A });
    second.send({ t: 'subscribe', url: URL_A });
    const source = FakeEventSource.instances[0]!;

    first.send({ t: 'unsubscribe', url: URL_A });
    expect(source.closed).toBe(false);

    second.send({ t: 'unsubscribe', url: URL_A });
    expect(source.closed).toBe(true);
  });

  it('reopens a torn-down stream for the next subscriber', async () => {
    const hub = await loadHub();
    const first = connect(hub);
    first.send({ t: 'subscribe', url: URL_A });
    first.send({ t: 'unsubscribe', url: URL_A });

    const second = connect(hub);
    second.send({ t: 'subscribe', url: URL_A });

    // Two constructions, not one: the stream table must have forgotten the
    // closed source. Reusing a closed EventSource would leave every later
    // page with a stream that never delivers.
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(FakeEventSource.instances[1]?.closed).toBe(false);
  });

  it('tears the stream down on error rather than leaving two retry loops on one socket', async () => {
    const hub = await loadHub();
    const first = connect(hub);
    first.send({ t: 'subscribe', url: URL_A });
    const source = FakeEventSource.instances[0]!;

    source.onerror?.();

    expect(source.closed).toBe(true);
    expect(first.sent).toContainEqual({ t: 'error' });
    // The subscriber owns the backoff (useGcEvents.ts), so its resubscribe is
    // what reopens the stream. If the hub kept the dead entry, that
    // resubscribe would attach to a closed source and the page would never
    // recover.
    first.send({ t: 'subscribe', url: URL_A });
    expect(FakeEventSource.instances).toHaveLength(2);
  });
});
