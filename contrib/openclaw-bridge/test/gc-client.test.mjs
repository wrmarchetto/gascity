// Smoke coverage for the shared gc transport glue (lib/gc-client.mjs) that both
// bridges now depend on. The important contract here is gcFetch's error shape:
// inbound failure classification (lib/inbound.mjs) keys off err.status, so a
// non-2xx must surface the HTTP status and a transport error must not.
//
// Run: npm test  (or: node --test test/)

import { test } from 'node:test'
import assert from 'node:assert/strict'
import http from 'node:http'
import { cityName, env, makeGcClient, startCallbackServer, makeAdapterRegistrar, makeNamedSessionBinder } from '../lib/gc-client.mjs'

// listen starts a one-off server on an ephemeral port and resolves { server, port }.
function listen(handler) {
  return new Promise((resolve) => {
    const server = http.createServer(handler)
    server.listen(0, '127.0.0.1', () => resolve({ server, port: server.address().port }))
  })
}
const close = (server) => new Promise((resolve) => server.close(resolve))

test('env returns the value when set and non-empty, else the default', () => {
  process.env.__GC_TEST_A = 'x'
  process.env.__GC_TEST_B = ''
  try {
    assert.equal(env('__GC_TEST_A', 'd'), 'x')
    assert.equal(env('__GC_TEST_B', 'd'), 'd') // empty string falls back to the default
    assert.equal(env('__GC_TEST_MISSING', 'd'), 'd')
  } finally {
    delete process.env.__GC_TEST_A
    delete process.env.__GC_TEST_B
  }
})

test('gcFetch posts to the URL-encoded city path with the CSRF header and parses JSON', async () => {
  const seen = []
  const { server, port } = await listen((req, res) => {
    const chunks = []
    req.on('data', (c) => chunks.push(c))
    req.on('end', () => {
      seen.push({
        method: req.method,
        url: req.url,
        csrf: req.headers['x-gc-request'],
        body: Buffer.concat(chunks).toString('utf8'),
      })
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify({ ok: true }))
    })
  })
  try {
    const { gcFetch } = makeGcClient({ baseUrl: `http://127.0.0.1:${port}`, city: 'my city' })
    const out = await gcFetch('POST', '/extmsg/inbound', { hello: 'world' })
    assert.deepEqual(out, { ok: true })
    assert.equal(seen.length, 1)
    assert.equal(seen[0].method, 'POST')
    assert.equal(seen[0].url, '/v0/city/my%20city/extmsg/inbound') // city is URL-encoded
    assert.equal(seen[0].csrf, '1')
    assert.deepEqual(JSON.parse(seen[0].body), { hello: 'world' })
  } finally {
    await close(server)
  }
})

test('gcFetch throws with the HTTP status attached so callers can classify failures', async () => {
  const { server, port } = await listen((req, res) => {
    res.writeHead(422, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ error: 'normalized inbound rejected' }))
  })
  try {
    const { gcFetch } = makeGcClient({ baseUrl: `http://127.0.0.1:${port}`, city: 'c' })
    await assert.rejects(
      () => gcFetch('POST', '/extmsg/inbound', {}),
      (err) => {
        assert.equal(err.status, 422)
        assert.match(err.message, /HTTP 422/)
        return true
      },
    )
  } finally {
    await close(server)
  }
})

test('gcFetch surfaces transport errors with no numeric status (treated as transient)', async () => {
  // Bind a server, take its port, then close it so the connect is refused.
  const { server, port } = await listen(() => {})
  await close(server)
  const { gcFetch } = makeGcClient({ baseUrl: `http://127.0.0.1:${port}`, city: 'c' })
  await assert.rejects(
    () => gcFetch('POST', '/extmsg/inbound', {}),
    (err) => {
      assert.equal(typeof err.status, 'undefined') // no HTTP status -> inbound classifier holds for redelivery
      return true
    },
  )
})

test('startCallbackServer serves /healthz, routes to handleRequest, 404s unknown paths, 500s a thrown handler', async () => {
  const handleRequest = async (req, rawBody) => {
    if (req.method === 'POST' && req.url === '/publish') {
      return { status: 200, body: { delivered: true, echo: JSON.parse(rawBody) } }
    }
    if (req.method === 'POST' && req.url === '/boom') throw new Error('handler exploded')
    return { status: 404, body: { error: 'not found' } }
  }
  const server = await startCallbackServer({ handleRequest, port: 0 })
  const base = `http://127.0.0.1:${server.address().port}`
  try {
    const health = await fetch(`${base}/healthz`)
    assert.equal(health.status, 200)
    assert.deepEqual(await health.json(), { ok: true })

    const pub = await fetch(`${base}/publish`, { method: 'POST', body: JSON.stringify({ text: 'hi' }) })
    assert.equal(pub.status, 200)
    assert.deepEqual(await pub.json(), { delivered: true, echo: { text: 'hi' } })

    const missing = await fetch(`${base}/nope`, { method: 'POST', body: '{}' })
    assert.equal(missing.status, 404)

    const boom = await fetch(`${base}/boom`, { method: 'POST', body: '{}' })
    assert.equal(boom.status, 500)
    assert.match((await boom.json()).error, /exploded/)
  } finally {
    await close(server)
  }
})

test('makeAdapterRegistrar registers and unregisters with the gc-facing body shape', async () => {
  const calls = []
  const { server, port } = await listen((req, res) => {
    const chunks = []
    req.on('data', (c) => chunks.push(c))
    req.on('end', () => {
      calls.push({ method: req.method, url: req.url, body: Buffer.concat(chunks).toString('utf8') })
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end('{}')
    })
  })
  try {
    const base = `http://127.0.0.1:${port}`
    const { gcFetch } = makeGcClient({ baseUrl: base, city: 'c' })
    const registrar = makeAdapterRegistrar({
      gcFetch,
      baseUrl: base,
      provider: 'telegram',
      account: 'default',
      name: 'openclaw-telegram-bridge',
      callbackUrl: 'http://127.0.0.1:8931',
      capabilities: { SupportsChildConversations: true, SupportsAttachments: false, MaxMessageLength: 0 },
      log: () => {},
    })
    await registrar.registerWithRetry()
    await registrar.unregister()

    const reg = calls.find((c) => c.method === 'POST' && c.url === '/v0/city/c/extmsg/adapters')
    assert.ok(reg, 'posted a registration')
    assert.deepEqual(JSON.parse(reg.body), {
      provider: 'telegram',
      account_id: 'default',
      name: 'openclaw-telegram-bridge',
      callback_url: 'http://127.0.0.1:8931',
      capabilities: { SupportsChildConversations: true, SupportsAttachments: false, MaxMessageLength: 0 },
    })

    const del = calls.find((c) => c.method === 'DELETE' && c.url === '/v0/city/c/extmsg/adapters')
    assert.ok(del, 'deleted the registration on unregister')
    assert.deepEqual(JSON.parse(del.body), { provider: 'telegram', account_id: 'default' })
  } finally {
    await close(server)
  }
})

test('scheduled re-registration restores an adapter after a controller restart clears its registry', async () => {
  let registered = false
  let reregisterTick
  let scheduledEvery
  const { server, port } = await listen((req, res) => {
    const chunks = []
    req.on('data', (chunk) => chunks.push(chunk))
    req.on('end', () => {
      if (req.method === 'POST' && req.url === '/v0/city/lab/extmsg/adapters') {
        registered = true
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end('{}')
        return
      }
      if (req.method === 'POST' && req.url === '/v0/city/lab/extmsg/outbound') {
        if (!registered) {
          res.writeHead(503, { 'Content-Type': 'application/json' })
          res.end(JSON.stringify({ error: 'adapter unavailable after controller restart' }))
          return
        }
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ delivered: true }))
        return
      }
      res.writeHead(404)
      res.end()
    })
  })
  try {
    const { gcFetch } = makeGcClient({ baseUrl: `http://127.0.0.1:${port}`, city: 'lab' })
    const registrar = makeAdapterRegistrar({
      gcFetch,
      baseUrl: `http://127.0.0.1:${port}`,
      provider: 'slack',
      account: 'default',
      name: 'slack-socket-mode-bridge',
      callbackUrl: 'http://127.0.0.1:8932',
      capabilities: { SupportsChildConversations: false, SupportsAttachments: false, MaxMessageLength: 40000 },
      log: () => {},
      reregisterMs: 25,
      setIntervalFn: (fn, ms) => {
        reregisterTick = fn
        scheduledEvery = ms
        return { unref() {} }
      },
    })

    await registrar.registerWithRetry()
    registered = false // controller restart: its adapter registry is in-memory
    await assert.rejects(() => gcFetch('POST', '/extmsg/outbound', { text: 'before re-registration' }), { status: 503 })

    const timer = registrar.startReregister()
    assert.ok(timer)
    assert.equal(scheduledEvery, 25)
    await reregisterTick()

    assert.deepEqual(await gcFetch('POST', '/extmsg/outbound', { text: 'after re-registration' }), { delivered: true })
  } finally {
    await close(server)
  }
})

test('makeNamedSessionBinder binds an adapter conversation to the configured agent identity', async () => {
  const calls = []
  const { server, port } = await listen((req, res) => {
    const chunks = []
    req.on('data', (c) => chunks.push(c))
    req.on('end', () => {
      calls.push({ method: req.method, url: req.url, body: Buffer.concat(chunks).toString('utf8') })
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end('{}')
    })
  })
  try {
    const { gcFetch } = makeGcClient({ baseUrl: `http://127.0.0.1:${port}`, city: 'lab' })
    const conversation = {
      scope_id: 'lab',
      provider: 'slack',
      account_id: 'team-1',
      conversation_id: 'C012345',
      kind: 'room',
    }
    await makeNamedSessionBinder({ gcFetch, conversation, agentName: 'lab/lead', log: () => {} }).bindWithRetry()

    assert.deepEqual(calls, [
      {
        method: 'POST',
        url: '/v0/city/lab/extmsg/bind',
        body: JSON.stringify({ conversation, agent_name: 'lab/lead' }),
      },
    ])
  } finally {
    await close(server)
  }
})

// A 409 from /extmsg/bind means the conversation is actively bound to a
// DIFFERENT target. No amount of waiting changes that, and gc will not hand
// the conversation over without replace=true, so the old behavior -- retry any
// error 60 times at 1s then throw -- turned a permanent, actionable refusal
// into 60s of silence followed by a process exit. Because the throw lands on a
// top-level await in the bridge entrypoints, the supervisor restarts straight
// back into the same 409: a crash loop whose only visible symptom is a
// restarting service. The operator sequence that produces it is ordinary --
// stop the bridge, repoint it at another agent, start it.
//
// The retry count is asserted, not just the throw. A fix that still burned the
// 60 attempts and merely rewrote the final message would satisfy an
// assertion on the error alone.
test('makeNamedSessionBinder does not retry a 409 conflict and names the remedy', async () => {
  let attempts = 0
  const { server, port } = await listen((req, res) => {
    req.resume()
    req.on('end', () => {
      attempts += 1
      res.writeHead(409, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify({ detail: 'conversation already bound to agent lab/other' }))
    })
  })
  try {
    const { gcFetch } = makeGcClient({ baseUrl: `http://127.0.0.1:${port}`, city: 'lab' })
    const logged = []
    const binder = makeNamedSessionBinder({
      gcFetch,
      conversation: { scope_id: 'lab', provider: 'slack', account_id: 'team-1', conversation_id: 'C1', kind: 'room' },
      agentName: 'lab/lead',
      log: (...a) => logged.push(a.join(' ')),
    })

    const err = await binder.bindWithRetry().then(
      () => null,
      (e) => e,
    )
    assert.ok(err, 'a 409 must reject rather than resolve')
    assert.equal(attempts, 1, 'a permanent conflict must not be retried')
    assert.equal(err.status, 409)
    assert.match(err.message, /already bound/)
    assert.match(err.message, /gc extmsg handoff/, 'the error must name the command that resolves it')
  } finally {
    await close(server)
  }
})

// The companion assertion: classification must not swallow the transient case
// the retry budget exists for. gc is routinely still starting when a bridge
// comes up, and that arrives as a 5xx or a transport error, not a 4xx. Without
// this test a fix could make every failure permanent and the suite would still
// be green on the case above.
test('makeNamedSessionBinder still retries a transient failure', async () => {
  let attempts = 0
  const { server, port } = await listen((req, res) => {
    req.resume()
    req.on('end', () => {
      attempts += 1
      if (attempts < 3) {
        res.writeHead(503, { 'Content-Type': 'application/json' })
        res.end('{"detail":"starting"}')
        return
      }
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end('{"agent_name":"lab/lead"}')
    })
  })
  try {
    const { gcFetch } = makeGcClient({ baseUrl: `http://127.0.0.1:${port}`, city: 'lab' })
    const binder = makeNamedSessionBinder({
      gcFetch,
      conversation: { scope_id: 'lab', provider: 'slack', account_id: 'team-1', conversation_id: 'C1', kind: 'room' },
      agentName: 'lab/lead',
      log: () => {},
      retryDelayMs: 0,
    })
    assert.deepEqual(await binder.bindWithRetry(), { agent_name: 'lab/lead' })
    assert.equal(attempts, 3)
  } finally {
    await close(server)
  }
})

// --- city name resolution ---
//
// These pin the seam that produced ci-azvlhn: gc's launchers define GC_CITY as
// the city PATH (internal/citylayout/runtime.go sets GC_CITY and GC_CITY_PATH
// to the same city root), while every /v0/city/{cityName}/... route resolves
// through a name-keyed registry. The bridges used to read GC_CITY as a name,
// so under [[service]] supervision they sent the path and 404ed once a second
// for two hours. Asserted here rather than in the entrypoints because all four
// executables share this resolution through lib/gc-client.mjs.

test('cityName prefers GC_CITY_NAME over the path-valued GC_CITY', () => {
  assert.equal(cityName({ GC_CITY_NAME: 'city', GC_CITY: '/home/willie/projects/city' }), 'city')
})

test('cityName falls back to GC_CITY for the documented hand-run form', () => {
  // README: `GC_CITY=lab node slack-bridge.mjs`. Dropping this fallback would
  // break every hand-run invocation and every demo script.
  assert.equal(cityName({ GC_CITY: 'lab' }), 'lab')
})

test('cityName rejects a GC_CITY that is a path, naming the remedy', () => {
  // Fails at startup instead of retrying a URL that can never resolve: a city
  // name is one URL segment, so a value containing "/" is not a name and no
  // amount of retrying makes it one. This is the exact live value.
  assert.throws(() => cityName({ GC_CITY: '/home/willie/projects/city' }), (err) => {
    assert.match(err.message, /GC_CITY_NAME/)
    return true
  })
})

test('cityName rejects an empty environment rather than requesting /v0/city//', () => {
  // An empty name builds /v0/city//extmsg/adapters, which the router answers
  // with a 307 to a different path -- a failure mode even harder to read than
  // the 404 it replaced.
  assert.throws(() => cityName({}), (err) => {
    assert.match(err.message, /GC_CITY_NAME/)
    return true
  })
})

test('cityName ignores an empty GC_CITY_NAME and falls through to GC_CITY', () => {
  // proxy_process exports GC_CITY_NAME unconditionally, so a launcher that
  // cannot determine the name exports it EMPTY rather than omitting it. An
  // empty value must not shadow a usable GC_CITY.
  assert.equal(cityName({ GC_CITY_NAME: '', GC_CITY: 'lab' }), 'lab')
})
