// One city event stream per browser profile, shared by every open dashboard
// page.
//
// Why a SharedWorker and not a per-page EventSource: a browser gives an
// HTTP/1.1 origin six sockets, and a page that holds its own stream holds one
// of them for its whole life. Six open dashboard pages consumed all six, and
// every short data request issued after that -- in every page, including one
// just opened -- was queued in the browser and never sent. The operator saw a
// city named `(unknown)` and panels stuck loading, while the server answered
// each of those endpoints in microseconds and logged nothing (measured
// 2026-10-12, bead ci-s4mwuy: page 6 lost status, usage, runs/census,
// sessions and runs/summary; page 7's document request never got a socket).
//
// Rejected alternative: closing the stream on `visibilitychange` so only the
// looked-at tab holds a socket. It is a fifth of the code and it uses the
// convention already in useVisibleRefresh. It was rejected because NOTHING
// CAN PIN IT -- every Playwright page reports visibilityState 'visible'
// however many are open, headless and headed alike, and neither
// Emulation.setFocusEmulationEnabled nor Page.setWebLifecycleState changes
// that (all three measured 2026-10-12). The only way to reach the branch is
// to dispatch a forged visibilitychange, which pins the handler against a
// faked dependency rather than against a hidden tab. It is also weaker: it
// still fails for six side-by-side windows, which this does not.
//
// A SharedWorker is chosen over BroadcastChannel leader election because the
// browser guarantees exactly one instance per origin per profile and tears it
// down when the last page goes. Leader election would have to re-implement
// that with heartbeats and takeover, and a wrong election leaves the city
// with no stream at all -- worse than the defect.
//
// This worker never PARSES a payload and never retries. Both are deliberate
// absences: the subscriber owns the malformed-payload report (it is the side
// that can reach the client-error endpoint) and owns the reconnect backoff
// (useGcEvents.ts). Adding EventSource's own reconnect here as well would
// give one socket two independent retry loops.
//
// Invariant: at most one EventSource per stream URL for the whole profile.
// Verified by e2e/connection-budget.spec.ts.

export type HubOutbound = { t: 'subscribe'; url: string } | { t: 'unsubscribe'; url: string };

export type HubInbound = { t: 'open' } | { t: 'error' } | { t: 'event'; data: string };

// The one member of SharedWorkerGlobalScope this file needs, declared here
// rather than pulled in with `/// <reference lib="webworker" />`. That
// reference is program-wide, not file-scoped: it would load the webworker lib
// alongside the DOM lib the rest of the SPA compiles against and collide on
// every name the two both declare.
interface SharedWorkerConnectScope {
  onconnect: ((event: MessageEvent) => void) | null;
}

interface Stream {
  source: EventSource;
  ports: Set<MessagePort>;
}

const streams = new Map<string, Stream>();

function post(port: MessagePort, message: HubInbound): void {
  try {
    port.postMessage(message);
  } catch {
    // The page went away between the fan-out and this send. Dropping it is
    // correct: the port is dead either way, and `drop` reaps it when its page
    // unsubscribes or the stream is torn down.
  }
}

/**
 * drop removes a port from a stream and closes the underlying EventSource
 * once the last subscriber leaves, so a profile with no open dashboard page
 * holds no socket.
 */
function drop(url: string, port: MessagePort): void {
  const stream = streams.get(url);
  if (stream === undefined) return;
  stream.ports.delete(port);
  if (stream.ports.size > 0) return;
  stream.source.close();
  streams.delete(url);
}

/**
 * ensure attaches `port` to the stream for `url`, opening it if this is the
 * first subscriber.
 */
function ensure(url: string, port: MessagePort): void {
  const existing = streams.get(url);
  if (existing !== undefined) {
    existing.ports.add(port);
    // A late joiner gets no replay: the stream is already past those events,
    // and the subscriber's contract is "something changed, refetch", so a
    // missed event costs at most one refresh it would have made anyway.
    // Replaying would need a per-stream ring and a cursor per port, to
    // deliver refreshes the joiner's own first fetch already covers. Say the
    // stream is open so the joiner does not sit in `connecting` until the
    // next event happens to arrive.
    if (existing.source.readyState === existing.source.OPEN) post(port, { t: 'open' });
    return;
  }

  const source = new EventSource(url);
  const stream: Stream = { source, ports: new Set([port]) };
  streams.set(url, stream);

  const fanout = (message: HubInbound) => {
    for (const p of stream.ports) post(p, message);
  };

  source.onopen = () => fanout({ t: 'open' });
  // gc names its events `event` rather than leaving them unnamed, so only
  // addEventListener('event') receives them; onmessage covers an unnamed one.
  const forward = (msg: MessageEvent<string>) => fanout({ t: 'event', data: msg.data });
  source.onmessage = forward;
  source.addEventListener('event', forward as EventListener);
  source.onerror = () => {
    const ports = [...stream.ports];
    source.close();
    streams.delete(url);
    for (const p of ports) post(p, { t: 'error' });
  };
}

// `onconnect` fires once per connecting port. MessagePort has no close event,
// so a page that crashes without unsubscribing leaves a port behind and its
// stream stays open with no reader. That leak is bounded at ONE socket for
// the whole profile -- the thing this worker exists to guarantee -- and the
// next dashboard page reuses that same stream rather than opening another.
(self as unknown as SharedWorkerConnectScope).onconnect = (event: MessageEvent) => {
  const port = event.ports[0];
  if (port === undefined) return;
  port.onmessage = (message: MessageEvent<HubOutbound>) => {
    const payload = message.data;
    if (payload.t === 'subscribe') {
      ensure(payload.url, port);
      return;
    }
    drop(payload.url, port);
  };
  port.start();
};
