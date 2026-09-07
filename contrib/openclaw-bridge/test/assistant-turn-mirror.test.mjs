import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createAssistantTurnMirror, streamAssistantTurns } from '../lib/assistant-turn-mirror.mjs'

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

test('mirrors a final assistant text turn through extmsg outbound without an agent reply action', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({
    conversation,
    sessionID: 'session-mayor',
    publish: async (body) => calls.push(body),
  })

  await mirror.handleStructuredEvent(structured([assistantText('assistant-1', 'I found the issue and fixed it.')]))

  assert.deepEqual(calls, [{
    session_id: 'session-mayor',
    conversation,
    text: 'I found the issue and fixed it.',
    idempotency_key: 'assistant-turn:assistant-1:1',
  }])
})

test('does not mirror a structured tool result', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-mayor', publish: async (body) => calls.push(body) })

  await mirror.handleStructuredEvent(structured([{
    id: 'tool-result-1',
    role: 'tool',
    status: 'final',
    blocks: [{ type: 'tool_result', content: 'sensitive command output' }],
  }]))

  assert.deepEqual(calls, [])
})

test('does not mirror assistant tool-use blocks', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-mayor', publish: async (body) => calls.push(body) })

  await mirror.handleStructuredEvent(structured([{
    id: 'tool-use-1',
    role: 'assistant',
    status: 'final',
    blocks: [{ type: 'tool_use', name: 'shell', input: { command: 'secret-command' } }],
  }]))

  assert.deepEqual(calls, [])
})

test('does not mirror inbound traffic from another agent', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-mayor', publish: async (body) => calls.push(body) })

  await mirror.handleStructuredEvent(structured([{
    id: 'agent-message-1',
    role: 'user',
    status: 'final',
    blocks: [{ type: 'text', text: 'another agent says hello' }],
  }]))

  assert.deepEqual(calls, [])
})

test('does not re-mirror a Slack channel message already visible to its sender', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-mayor', publish: async (body) => calls.push(body) })

  await mirror.handleStructuredEvent(structured([{
    id: 'slack-inbound-1',
    role: 'user',
    status: 'final',
    user_prompt: { text: 'Willie posted this in Slack' },
    blocks: [{ type: 'text', text: 'Willie posted this in Slack' }],
  }]))

  assert.deepEqual(calls, [])
})

test('mirrors a long assistant turn as labeled lossless chunks instead of dropping it', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({
    conversation,
    sessionID: 'session-mayor',
    maxMessageLength: 24,
    publish: async (body) => calls.push(body),
  })
  const text = 'abcdefghijklmnopqrstuvwxy🙂z'

  await mirror.handleStructuredEvent(structured([assistantText('assistant-long', text)]))

  assert.ok(calls.length > 1, 'long turn is chunked')
  assert.ok(calls.every(({ text: chunk }) => chunk.length <= 24), 'each chunk fits the configured limit')
  assert.match(calls[0].text, /^\[part 1\/\d+\]\n/)
  assert.match(calls.at(-1).text, new RegExp(`^\\[part ${calls.length}\\/${calls.length}\\]\\n`))
  assert.equal(calls.map(({ text: chunk }) => chunk.replace(/^\[part \d+\/\d+\]\n/, '')).join(''), text)
})

test('does not duplicate a final assistant turn replayed by SSE snapshot and upsert frames', async () => {
  const calls = []
  const mirror = createAssistantTurnMirror({ conversation, sessionID: 'session-mayor', publish: async (body) => calls.push(body) })
  const event = structured([assistantText('assistant-stable-id', 'one durable answer')])

  await mirror.handleStructuredEvent({ ...event, operation: 'snapshot' })
  await mirror.handleStructuredEvent({ ...event, operation: 'upsert' })

  assert.deepEqual(calls.map(({ text }) => text), ['one durable answer'])
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
