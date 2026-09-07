import { test } from 'node:test'
import assert from 'node:assert/strict'
import { makeSlackForward, makeSlackSocketHandler, normalizeSlackMessage } from '../lib/slack.mjs'

const options = {
  channelID: 'C012345',
  scopeID: 'lab',
  provider: 'slack',
  accountID: 'team-1',
}

test('normalizeSlackMessage preserves a channel message\'s complete text for extmsg inbound', () => {
  const message = normalizeSlackMessage(
    {
      channel: 'C012345',
      client_msg_id: 'e4c9b5e2-1b2c-4a9e-a6da-1805624f071d',
      ts: '0.123',
      user: 'U123',
      text: 'please inspect <https://example.test|this>\nand report back',
    },
    options,
  )

  assert.deepEqual(message, {
    provider_message_id: 'e4c9b5e2-1b2c-4a9e-a6da-1805624f071d',
    conversation: {
      scope_id: 'lab',
      provider: 'slack',
      account_id: 'team-1',
      conversation_id: 'C012345',
      kind: 'room',
    },
    actor: { id: 'U123', display_name: 'U123', is_bot: false },
    text: 'please inspect <https://example.test|this>\nand report back',
    received_at: '1970-01-01T00:00:00.123Z',
  })
})

test('normalizeSlackMessage rejects a different channel, bot traffic, and message subtypes', () => {
  const base = { channel: 'C012345', client_msg_id: 'm-1', ts: '1', user: 'U1', text: 'hello' }

  assert.equal(normalizeSlackMessage({ ...base, channel: 'C-other' }, options), null)
  assert.equal(normalizeSlackMessage({ ...base, bot_id: 'B1' }, options), null)
  assert.equal(normalizeSlackMessage({ ...base, subtype: 'message_changed' }, options), null)
})

test('Socket Mode handler acknowledges before forwarding the normalized message', async () => {
  const calls = []
  const handler = makeSlackSocketHandler({
    ...options,
    forward: async (message) => calls.push(['forward', message]),
  })

  await handler({
    event: { channel: 'C012345', client_msg_id: 'm-2', ts: '2', user: 'U2', text: 'full text' },
    ack: async () => calls.push(['ack']),
  })

  assert.equal(calls[0][0], 'ack')
  assert.deepEqual(calls[1], [
    'forward',
    {
      provider_message_id: 'm-2',
      conversation: {
        scope_id: 'lab',
        provider: 'slack',
        account_id: 'team-1',
        conversation_id: 'C012345',
        kind: 'room',
      },
      actor: { id: 'U2', display_name: 'U2', is_bot: false },
      text: 'full text',
      received_at: '1970-01-01T00:00:02.000Z',
    },
  ])
})

test('makeSlackForward posts the normalized message unchanged to extmsg inbound', async () => {
  const calls = []
  const forward = makeSlackForward({
    gcFetch: async (...args) => calls.push(args),
    isShuttingDown: () => false,
    sleep: async () => {},
  })
  const message = normalizeSlackMessage(
    { channel: 'C012345', client_msg_id: 'm-3', ts: '3', user: 'U3', text: 'the complete message' },
    options,
  )

  assert.equal(await forward(message), true)
  assert.deepEqual(calls, [['POST', '/extmsg/inbound', { message }]])
})
