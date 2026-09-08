import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createAssistantTurnMirror, reconnectAssistantTurnStream, streamAssistantTurns } from '../lib/assistant-turn-mirror.mjs'

// Scope: the assistant-turn mirror's frame consumer -- which turns it publishes
// and which it refuses -- plus the chunking policy and the reconnect loop.
// Everything here is a pure exercise of lib/assistant-turn-mirror.mjs against
// hand-built frames; the entrypoint's env handling belongs to
// test/entrypoints.test.mjs and the outbound wire to test/gc-client.test.mjs.
//
// WHAT THIS SUITE CANNOT REPRESENT, so do not read a green run as covering it:
// the frames are hand-built, so the suite is only as right as this file's
// reading of what the server emits. Two arms cover that separately and neither
// lives here -- a dry run of the real consumer against a live
// /session/{id}/stream with the outbound leg stubbed, and the same live frame
// replayed with only its `operation` varied. Both are recorded in the commit
// that added the watermark (bead gs-t0d8), and both should be redone by hand if
// the structured-stream contract changes.
//
// Run: npm --prefix contrib/openclaw-bridge test
// (or `make test-openclaw-bridge`, which installs dependencies first -- without
// them four unrelated entrypoint tests fail on a missing @slack/socket-mode).
//
// Fixture identities are named after what they test, never after a role. The
// mirror binds whatever session id it is configured with, so a fixture named
// for a role would read as the bridge knowing about one, which AGENTS.md
// forbids (ZERO hardcoded roles). scripts/check-extmsg-bridge-isolation.sh
// refuses the name in a comment as readily as in code: it scans text and
// cannot tell prose from a lookup key, which is why it is not spelled here.

const conversation = {
  scope_id: 'lab',
  provider: 'slack',
  account_id: 'team-1',
  conversation_id: 'C012345',
  kind: 'room',
}

const assistantText = (id, text) => ({
  id,
  role: 'assistant',
  status: 'final',
  blocks: [{ type: 'text', text }],
})

const structured = (messages) => ({ structured_messages: messages })

// attach(mirror) walks a consumer past its attach watermark with an empty
// snapshot. Every test below that expects a publish goes through it, and so
// does every exclusion test: after the watermark landed, an exclusion driven as
// the consumer's FIRST frame would be satisfied by the watermark rather than by
// the filter it means to pin, and would stay green with the filter deleted.
const attach = async (mirror) => mirror.handleStructuredEvent({ operation: 'snapshot', structured_messages: [] })

// A frame that extends the transcript rather than restating it -- what the
// stream sends once its per-connection resume cursor is established.
const upsert = (messages) => ({ operation: 'upsert', structured_messages: messages })

test('mirrors a final assistant text turn through extmsg outbound without an agent reply action', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({
    conversation,
    sessionID: 'session-under-test',
    publish: async (body) => calls.push(body),
  })
  await attach(mirror)

  await mirror.handleStructuredEvent(upsert([assistantText('assistant-1', 'I found the issue and fixed it.')]))

  assert.deepEqual(calls, [{
    session_id: 'session-under-test',
    conversation,
    text: 'I found the issue and fixed it.',
    idempotency_key: 'assistant-turn:assistant-1:1',
  }])
})

test('does not mirror a structured tool result', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-under-test', publish: async (body) => calls.push(body) })
  await attach(mirror)

  await mirror.handleStructuredEvent(upsert([{
    id: 'tool-result-1',
    role: 'tool',
    status: 'final',
    blocks: [{ type: 'tool_result', content: 'sensitive command output' }],
  }]))

  assert.deepEqual(calls, [], 'signature: a tool result must never reach the channel')
})

test('does not mirror assistant tool-use blocks', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-under-test', publish: async (body) => calls.push(body) })
  await attach(mirror)

  await mirror.handleStructuredEvent(upsert([{
    id: 'tool-use-1',
    role: 'assistant',
    status: 'final',
    blocks: [{ type: 'tool_use', name: 'shell', input: { command: 'secret-command' } }],
  }]))

  assert.deepEqual(calls, [], 'signature: a tool_use block must never reach the channel')
})

test('does not mirror inbound traffic from another agent', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-under-test', publish: async (body) => calls.push(body) })
  await attach(mirror)

  await mirror.handleStructuredEvent(upsert([{
    id: 'agent-message-1',
    role: 'user',
    status: 'final',
    blocks: [{ type: 'text', text: 'another agent says hello' }],
  }]))

  assert.deepEqual(calls, [], 'signature: a foreign inbound message must never be mirrored')
})

test('does not re-mirror a Slack channel message already visible to its sender', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-under-test', publish: async (body) => calls.push(body) })
  await attach(mirror)

  await mirror.handleStructuredEvent(upsert([{
    id: 'slack-inbound-1',
    role: 'user',
    status: 'final',
    user_prompt: { text: 'Willie posted this in Slack' },
    blocks: [{ type: 'text', text: 'Willie posted this in Slack' }],
  }]))

  assert.deepEqual(calls, [], 'signature: an already-visible channel message must not be reflected')
})

test('does not mirror a non-text block that carries text of its own', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-under-test', publish: async (body) => calls.push(body) })
  await attach(mirror)

  // The block projection sets .text on more than text blocks. An image block
  // gets it, and so does EVERY block kind gc does not model yet, through the
  // default branch of historyBlockToStructuredBlock
  // (internal/api/session_structured_types.go) that copies text, content, name
  // and tool input alike. So the filter has to key on type === 'text'; keyed on
  // the presence of .text it publishes both, and the second case arrives
  // silently the first time a provider emits a block kind gc has not modelled.
  await mirror.handleStructuredEvent(upsert([{
    id: 'non-text-blocks-1',
    role: 'assistant',
    status: 'final',
    blocks: [
      { type: 'image', text: 'alt text of a captured screen', file_path: '/tmp/capture.png' },
      { type: 'a_kind_gc_does_not_model_yet', text: 'whatever the next provider puts here' },
    ],
  }]))

  assert.deepEqual(calls, [], 'signature: only a text block may reach the channel')
})

test('mirrors a long assistant turn as labeled lossless chunks instead of dropping it', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({
    conversation,
    sessionID: 'session-under-test',
    maxMessageLength: 24,
    publish: async (body) => calls.push(body),
  })
  await attach(mirror)
  const text = 'abcdefghijklmnopqrstuvwxy🙂z'

  await mirror.handleStructuredEvent(upsert([assistantText('assistant-long', text)]))

  assert.ok(calls.length > 1, 'long turn is chunked')
  assert.ok(calls.every(({ text: chunk }) => chunk.length <= 24), 'each chunk fits the configured limit')
  assert.match(calls[0].text, /^\[part 1\/\d+\]\n/)
  assert.match(calls.at(-1).text, new RegExp(`^\\[part ${calls.length}\\/${calls.length}\\]\\n`))
  assert.equal(calls.map(({ text: chunk }) => chunk.replace(/^\[part \d+\/\d+\]\n/, '')).join(''), text)
})

test("does not duplicate a turn an upsert's inclusive tail replays", async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-under-test', publish: async (body) => calls.push(body) })
  await attach(mirror)
  const turn = assistantText('assistant-stable-id', 'one durable answer')

  await mirror.handleStructuredEvent(upsert([turn]))
  // Each upsert restarts at the previous frame's last message so a partial can
  // become final under one stable id (session_structured_stream.go, the
  // start = previous.MessageCount - 1 branch), so a delivered turn returns.
  await mirror.handleStructuredEvent(upsert([turn, assistantText('assistant-next', 'and the next')]))

  assert.deepEqual(calls.map(({ text }) => text), ['one durable answer', 'and the next'], 'signature: a settled id must never publish twice')
})

test('consumes only structured SSE frames before passing them to the mirror filter', async () => {
  const seen = []
  const encoder = new TextEncoder()
  const response = new Response(new ReadableStream({
    start(controller) {
      controller.enqueue(encoder.encode(': keepalive\n\nevent: activity\ndata: {"activity":"idle"}\n\n'))
      controller.enqueue(encoder.encode('event: structured\ndata: {"structured_messages":[{"id":"a-1","role":"assistant","status":"final","blocks":[{"type":"text","text":"hello"}]}]}\n\n'))
      controller.close()
    },
  }))

  await streamAssistantTurns({ response, onStructuredEvent: async (event) => seen.push(event) })

  assert.deepEqual(seen, [structured([assistantText('a-1', 'hello')])])
})

test('a mirror pinned to a retired backing session goes quiet after respawn', async () => {
  const calls = []
  const controller = new AbortController()
  let liveSessionID = 's-before-respawn'
  let reconnects = 0
  const mirror = createAssistantTurnMirror({
    conversation,
    sessionID: 's-before-respawn',
    publish: async (body) => calls.push(body),
  })
  await attach(mirror)

  await reconnectAssistantTurnStream({
    sessionTarget: 's-before-respawn',
    signal: controller.signal,
    openStream: async (target) => {
      const text = target === liveSessionID ? 'before respawn' : ''
      return new Response(text === '' ? '' : `event: structured\ndata: ${JSON.stringify(upsert([assistantText(`turn-${liveSessionID}`, text)]))}\n\n`)
    },
    onStructuredEvent: mirror.handleStructuredEvent,
    waitForReconnect: async () => {
      reconnects += 1
      if (reconnects === 1) {
        liveSessionID = 's-after-respawn'
        return
      }
      controller.abort()
    },
  })

  assert.deepEqual(calls.map(({ text }) => text), ['before respawn'])
})

test('replays an unacknowledged assistant turn after controller restart makes outbound unavailable', async () => {
  const calls = []
  const controller = new AbortController()
  let publishes = 0
  const mirror = createAssistantTurnMirror({
    conversation,
    sessionID: 'lab/lead',
    publish: async (body) => {
      publishes += 1
      if (publishes === 1) throw Object.assign(new Error('adapter unavailable after controller restart'), { status: 503 })
      calls.push(body)
    },
  })
  await attach(mirror)
  // First delivery on a live connection, refused. Reconnecting opens a new
  // connection, whose first frame restates the transcript -- so the turn that
  // was never acknowledged has to survive the watermark that frame carries.
  const live = upsert([assistantText('replayed-turn', 'delivery resumed')])
  const reconnected = { operation: 'snapshot', structured_messages: [assistantText('replayed-turn', 'delivery resumed')] }
  let opens = 0

  await reconnectAssistantTurnStream({
    sessionTarget: 'lab/lead',
    signal: controller.signal,
    openStream: async () => {
      opens += 1
      return new Response(`event: structured\ndata: ${JSON.stringify(opens === 1 ? live : reconnected)}\n\n`)
    },
    onStructuredEvent: mirror.handleStructuredEvent,
    // Bounded on opens, not on a publish. Aborting from inside publish makes
    // the loop unreachable exactly when publishing stops, so a mutation that
    // mutes the consumer HANGS the suite instead of reddening it -- measured
    // against the watermark-always mutation, which spun until it was killed.
    waitForReconnect: async () => { if (opens >= 2) controller.abort() },
  })

  assert.equal(publishes, 2, 'signature: a refused turn must survive the restating frame')
  assert.deepEqual(calls.map(({ text }) => text), ['delivery resumed'])
})

test('reconnects through a stable named session after its backing session respawns', async () => {
  const calls = []
  const controller = new AbortController()
  const namedSession = 'lab/lead'
  let liveSessionID = 's-before-respawn'
  const responses = new Map([
    ['s-before-respawn', new Response(`event: structured\ndata: ${JSON.stringify(upsert([assistantText('old-turn', 'before respawn')]))}\n\n`)],
    ['s-after-respawn', new Response(`event: structured\ndata: ${JSON.stringify(upsert([assistantText('new-turn', 'after respawn')]))}\n\n`)],
  ])
  const openedTargets = []
  const mirror = createAssistantTurnMirror({
    conversation,
    sessionID: namedSession,
    publish: async (body) => calls.push(body),
  })
  await attach(mirror)

  await reconnectAssistantTurnStream({
    sessionTarget: namedSession,
    signal: controller.signal,
    openStream: async (target) => {
      openedTargets.push(target)
      const response = responses.get(liveSessionID)
      assert.ok(response, `no stream for resolved session ${liveSessionID}`)
      return response
    },
    onStructuredEvent: async (event) => {
      await mirror.handleStructuredEvent(event)
      if (event.structured_messages[0].id === 'new-turn') controller.abort()
    },
    waitForReconnect: async () => {
      liveSessionID = 's-after-respawn'
    },
  })

  assert.deepEqual(openedTargets, [namedSession, namedSession])
  assert.deepEqual(calls.map(({ text }) => text), ['before respawn', 'after respawn'])
})

// --- attach watermark ---
//
// The frames below are the shapes internal/api/session_structured_stream.go
// actually emits. A request carrying no resume cursor takes the empty-token
// branch of buildStructuredStreamUpdate and gets the WHOLE transcript as one
// operation:"snapshot"; later frames in that same connection are
// operation:"upsert" carrying only the new tail; a cursor the server can no
// longer honor answers operation:"reset" with the whole transcript again.

test('publishes nothing from the transcript that already existed when it attached', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-under-test', publish: async (body) => calls.push(body) })

  await mirror.handleStructuredEvent({
    operation: 'snapshot',
    structured_messages: [
      assistantText('backlog-1', 'a turn from an hour ago'),
      assistantText('backlog-2', 'a turn from a minute ago'),
      assistantText('backlog-3', 'the turn just before the consumer started'),
    ],
  })

  assert.deepEqual(calls, [], 'the attach snapshot is a watermark, never content')
})

test('publishes a turn that arrives after the attach watermark', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-under-test', publish: async (body) => calls.push(body) })

  await mirror.handleStructuredEvent({ operation: 'snapshot', structured_messages: [assistantText('backlog-1', 'before the consumer existed')] })
  await mirror.handleStructuredEvent(upsert([assistantText('backlog-1', 'before the consumer existed'), assistantText('live-1', 'this one is live')]))

  assert.deepEqual(calls.map(({ text }) => text), ['this one is live'], 'signature: a post-attach turn must still publish')
})

test('watermarks the transcript a reset frame restates rather than republishing it', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-under-test', publish: async (body) => calls.push(body) })

  await attach(mirror)
  await mirror.handleStructuredEvent(upsert([assistantText('live-1', 'delivered while the cursor held')]))
  // A respawned session or a rewritten transcript invalidates the cursor. The
  // frame that follows carries ids the consumer has never settled, so nothing
  // but the operation separates it from a transcript that genuinely grew.
  await mirror.handleStructuredEvent({
    operation: 'reset',
    reset_reason: 'stream_changed',
    structured_messages: [
      assistantText('rewritten-1', 'delivered while the cursor held'),
      assistantText('rewritten-2', 'and everything that preceded it'),
    ],
  })

  assert.deepEqual(calls.map(({ text }) => text), ['delivered while the cursor held'], 'signature: a reset restates, it does not grow')
})

test('retries a turn the adapter refused even when the retry arrives inside a restating frame', async () => {
  const calls = []
  let publishes = 0
  const mirror = createAssistantTurnMirror({
    conversation,
    sessionID: 'session-under-test',
    publish: async (body) => {
      publishes += 1
      if (publishes === 1) throw Object.assign(new Error('adapter unavailable'), { status: 503 })
      calls.push(body)
    },
  })

  await attach(mirror)
  // Accepted for delivery, then refused. The reconnect that follows opens a new
  // connection, so its first frame is a snapshot -- the same shape as the
  // attach watermark. A turn already accepted must survive it, or a transport
  // failure becomes silent loss.
  await assert.rejects(() => mirror.handleStructuredEvent(upsert([assistantText('refused-1', 'delivery resumed')])))
  await mirror.handleStructuredEvent({ operation: 'snapshot', structured_messages: [assistantText('refused-1', 'delivery resumed')] })

  assert.equal(publishes, 2, 'signature: a refused turn must survive the restating frame')
  assert.deepEqual(calls.map(({ text }) => text), ['delivery resumed'])
})

test('watermarks a reconnect snapshot instead of republishing the transcript it restates', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-under-test', publish: async (body) => calls.push(body) })

  await attach(mirror)
  await mirror.handleStructuredEvent(upsert([assistantText('live-1', 'seen live')]))
  // Reconnecting opens a new connection, so its first frame is another full
  // snapshot. gap-1 completed while nothing was reading and is GIVEN UP here:
  // the frame does not distinguish it from the backlog beside it, and the
  // softer rule that would publish it -- watermark only the first frame --
  // republishes a whole transcript whenever a respawned target reconnects under
  // fresh entry ids.
  await mirror.handleStructuredEvent({
    operation: 'snapshot',
    structured_messages: [
      assistantText('backlog-1', 'from before the consumer existed'),
      assistantText('live-1', 'seen live'),
      assistantText('gap-1', 'produced while the stream was down'),
    ],
  })
  await mirror.handleStructuredEvent(upsert([assistantText('gap-1', 'produced while the stream was down'), assistantText('live-2', 'seen live again')]))

  assert.deepEqual(calls.map(({ text }) => text), ['seen live', 'seen live again'], 'signature: a restated transcript must not republish')
})

test('watermarks a frame whose operation it does not recognize', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-under-test', publish: async (body) => calls.push(body) })

  await attach(mirror)
  // operation is required and enum-constrained on the wire, so an unrecognized
  // value means the wire changed under the consumer. It watermarks rather than
  // publishes: silence is recoverable and visible, a flood rate-limits a
  // channel this consumer shares with other publishers.
  await mirror.handleStructuredEvent({ operation: 'coalesce', structured_messages: [assistantText('unknown-op-1', 'from a wire this consumer does not know')] })

  assert.deepEqual(calls, [], 'signature: an unrecognized operation must not publish')
})
