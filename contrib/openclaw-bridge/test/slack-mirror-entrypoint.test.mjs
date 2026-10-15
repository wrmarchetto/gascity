// Scope: slack-mirror.mjs as a process, against a stand-in gc that serves one
// structured stream and one extmsg transcript and records every request. It
// pins the entrypoint's half of the answer-only gate -- that the gate is
// actually wired to GET /extmsg/transcript for the bound conversation and that
// its answer decides the POST -- which the lib suite cannot see, because it
// injects hasUnansweredInbound itself.
//
// The stand-in REFUSES every route it was not scripted for with a 599, so a
// request this file did not anticipate fails the run rather than succeeding.
//
// Ordering is taken from the stream, not from a timer: the stand-in closes the
// first stream after its frames, and the mirror only reconnects once it has
// finished handling them, so the second connection is the instant every
// publish decision for the first is already on the wire.
//
// WHAT THIS CANNOT REPRESENT: the real server's frames and transcript rows are
// hand-built here, in the shapes measured from the live API on 2026-10-15.
//
// Run: npm --prefix contrib/openclaw-bridge test

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import http from 'node:http'
import { fileURLToPath } from 'node:url'

const entrypoint = fileURLToPath(new URL('../slack-mirror.mjs', import.meta.url))
const SESSION = 'lab/lead'
const CHANNEL = 'C012345'

function frame(event) {
  return `event: structured\ndata: ${JSON.stringify(event)}\n\n`
}

// runMirror drives one stream's worth of frames through the process and
// resolves with every request the stand-in saw.
async function runMirror({ frames, transcriptItems }) {
  const seen = []
  let connections = 0
  let reconnected
  const secondConnection = new Promise((resolve) => { reconnected = resolve })
  const server = http.createServer((req, res) => {
    let body = ''
    req.on('data', (chunk) => { body += chunk })
    req.on('end', () => {
      const url = new URL(req.url, 'http://stand-in')
      seen.push({ method: req.method, path: url.pathname, query: url.searchParams, body })
      if (req.method === 'GET' && url.pathname === `/v0/city/lab/session/${encodeURIComponent(SESSION)}/stream`) {
        connections += 1
        res.writeHead(200, { 'Content-Type': 'text/event-stream' })
        if (connections === 1) {
          res.end(frames.map(frame).join(''))
        } else {
          reconnected()
        }
        return
      }
      if (req.method === 'GET' && url.pathname === '/v0/city/lab/extmsg/transcript') {
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ items: transcriptItems, total: transcriptItems.length }))
        return
      }
      if (req.method === 'POST' && url.pathname === '/v0/city/lab/extmsg/outbound') {
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end('{}')
        return
      }
      res.writeHead(599)
      res.end(`unscripted ${req.method} ${url.pathname}`)
    })
  })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  const child = spawn(process.execPath, [entrypoint], {
    env: {
      ...process.env,
      GC_CITY_NAME: 'lab',
      GC_MIRROR_SESSION: SESSION,
      BRIDGE_SLACK_CHANNEL_ID: CHANNEL,
      GC_BASE_URL: `http://127.0.0.1:${server.address().port}`,
    },
    stdio: ['ignore', 'ignore', 'pipe'],
  })
  let stderr = ''
  child.stderr.on('data', (chunk) => { stderr += chunk })
  const exited = new Promise((resolve) => child.on('exit', resolve))
  try {
    await Promise.race([
      secondConnection,
      exited.then((code) => { throw new Error(`slack-mirror exited ${code} before reconnecting:\n${stderr}`) }),
    ])
  } finally {
    child.kill('SIGTERM')
    await exited
    server.closeAllConnections()
    await new Promise((resolve) => server.close(resolve))
  }
  return { seen, stderr }
}

const snapshot = { operation: 'snapshot', structured_messages: [] }
const closingTurn = {
  operation: 'upsert',
  structured_messages: [{ id: 'closing-1', role: 'assistant', status: 'final', stop_reason: 'end_turn', blocks: [{ type: 'text', text: 'Nothing needs the reader.' }] }],
}
const conversationRows = (...kinds) => kinds.map((kind) => (kind === 'human'
  ? { Kind: 'inbound', SourceSessionID: '', Actor: { id: 'U0READER', is_bot: false } }
  : { Kind: 'outbound', SourceSessionID: SESSION, Actor: { id: '', is_bot: false } }))

test('the entrypoint asks the bound conversation newest first before deciding', async () => {
  const { seen, stderr } = await runMirror({ frames: [snapshot, closingTurn], transcriptItems: conversationRows('own') })

  const asks = seen.filter((r) => r.path.endsWith('/extmsg/transcript'))
  assert.equal(asks.length, 1, `signature: the gate must reach the transcript once per undecided message\n${stderr}`)
  assert.equal(asks[0].query.get('conversation_id'), CHANNEL)
  assert.equal(asks[0].query.get('order'), 'desc')
})

test('the entrypoint posts nothing for a turn no inbound message is waiting on', async () => {
  const { seen, stderr } = await runMirror({ frames: [snapshot, closingTurn], transcriptItems: conversationRows('own', 'human') })

  assert.deepEqual(seen.filter((r) => r.method === 'POST'), [], `signature: an answered conversation must get no post\n${stderr}`)
})

test('the entrypoint posts a turn exactly once when a human message waits on it', async () => {
  const { seen, stderr } = await runMirror({ frames: [snapshot, closingTurn], transcriptItems: conversationRows('human', 'own') })

  const posts = seen.filter((r) => r.method === 'POST')
  assert.equal(posts.length, 1, `signature: a waiting human message must get exactly one post\n${stderr}`)
  assert.equal(JSON.parse(posts[0].body).text, 'Nothing needs the reader.')
})
