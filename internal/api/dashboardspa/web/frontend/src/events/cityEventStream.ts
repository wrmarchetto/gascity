import type { HubInbound, HubOutbound } from './cityEventStream.sharedworker';
import CityEventStreamWorker from './cityEventStream.sharedworker?sharedworker';

// The page-side half of the shared city event stream. See
// cityEventStream.sharedworker.ts for why the stream is shared at all.
//
// This deliberately presents the SAME shape EventSource does -- onopen,
// onerror, onmessage, addEventListener('event'), close(), readyState -- so
// useGcEvents.ts keeps its connect/grace/backoff/degraded policy verbatim and
// only its transport changes. Replacing that policy at the same time would
// have made the socket fix and a rewrite of the reconnect logic one
// indivisible change, with no way to tell which of them a regression came
// from.

/** The subset of EventSource useGcEvents.ts uses. */
export interface CityEventStream {
  onopen: (() => void) | null;
  onerror: (() => void) | null;
  onmessage: ((event: MessageEvent<string>) => void) | null;
  addEventListener(type: 'event', listener: EventListener): void;
  readyState: number;
  close(): void;
}

// EventSource's own readyState constants, restated because an adapter is not
// an EventSource and `EventSource.CLOSED` is not guaranteed to exist in every
// environment the hook runs in -- the jsdom suites stub the global with a
// fake that carries no statics.
const CONNECTING = 0;
const OPEN = 1;
const CLOSED = 2;

// Set once a SharedWorker has failed, so the fallback is taken immediately
// for the rest of the page's life instead of being re-attempted on every
// reconnect. A browser that refuses the worker refuses it every time, and
// without this latch each of useGcEvents.ts's backoff retries would pay a
// failed construction before degrading.
let workerUnavailable = false;

/**
 * openCityEventStream subscribes to `url` through the shared worker, or
 * directly when the browser has no usable SharedWorker. It returns null when
 * neither transport exists, which is the hook's signal to report `closed`.
 *
 * The direct fallback is NOT dead code kept for a browser nobody uses: it is
 * also the path every jsdom test takes, because jsdom implements no
 * SharedWorker. Those suites therefore still exercise the hook against the
 * same `globalThis.EventSource` stub they always did.
 */
export function openCityEventStream(url: string): CityEventStream | null {
  const worker = tryConnectWorker();
  if (worker === null) return directStream(url);

  const namedListeners: EventListener[] = [];
  const stream: CityEventStream = {
    onopen: null,
    onerror: null,
    onmessage: null,
    addEventListener(type, listener) {
      if (type === 'event') namedListeners.push(listener);
    },
    readyState: CONNECTING,
    close() {
      if (stream.readyState === CLOSED) return;
      stream.readyState = CLOSED;
      worker.port.postMessage({ t: 'unsubscribe', url } satisfies HubOutbound);
      worker.port.close();
    },
  };

  worker.port.onmessage = (event: MessageEvent<HubInbound>) => {
    if (stream.readyState === CLOSED) return;
    const payload = event.data;
    if (payload.t === 'open') {
      stream.readyState = OPEN;
      stream.onopen?.();
      return;
    }
    if (payload.t === 'error') {
      // Left CONNECTING rather than CLOSED: the subscriber decides whether
      // this is fatal, exactly as it does for a real EventSource, and its
      // grace timer reads readyState to tell a slow connect from a dead one.
      stream.readyState = CONNECTING;
      stream.onerror?.();
      return;
    }
    // One MessageEvent per gc event, carrying the raw JSON string the server
    // sent. Re-wrapped rather than forwarded so the subscriber's handler sees
    // the `.data` string it expects from an EventSource.
    const forwarded = new MessageEvent('event', { data: payload.data });
    stream.onmessage?.(forwarded as MessageEvent<string>);
    for (const listener of namedListeners) listener(forwarded);
  };
  // A worker that fails to start reports it HERE, asynchronously, rather than
  // by throwing from the constructor -- which is how an unsupported script
  // type surfaces. Without this branch that failure is silent and the page
  // simply never refreshes again: no error, no reconnect, nothing to see.
  worker.onerror = () => {
    workerUnavailable = true;
    if (stream.readyState === CLOSED) return;
    stream.readyState = CONNECTING;
    stream.onerror?.();
  };
  worker.port.start();
  worker.port.postMessage({ t: 'subscribe', url } satisfies HubOutbound);
  return stream;
}

/**
 * tryConnectWorker returns a fresh port onto the shared worker, or null when
 * this browser has no SharedWorker or has already refused to start it.
 *
 * A failure here must never take the dashboard's live updates down with it,
 * so it degrades to a per-page stream. That costs the socket budget this
 * module exists to protect, which is strictly better than a city view that
 * never refreshes.
 */
function tryConnectWorker(): SharedWorker | null {
  if (workerUnavailable) return null;
  if (typeof SharedWorker !== 'function') return null;
  try {
    return new CityEventStreamWorker();
  } catch {
    workerUnavailable = true;
    return null;
  }
}

/**
 * directStream is the pre-worker behavior: this page owns the socket. Null
 * when the environment has no EventSource at all.
 */
function directStream(url: string): CityEventStream | null {
  const EventSourceCtor = globalThis.EventSource;
  if (typeof EventSourceCtor !== 'function') return null;
  return new EventSourceCtor(url) as unknown as CityEventStream;
}
