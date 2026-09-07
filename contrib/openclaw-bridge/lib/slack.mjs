// Slack-specific normalization for the Socket Mode bridge. Transport and
// routing live in gc; this module only translates a human Slack channel
// message into the pre-normalized extmsg inbound wire shape.

import { forwardWithRetry } from './inbound.mjs'

// normalizeSlackMessage returns null for events that must not enter the gc
// conversation: other channels, bot traffic, and Slack message subtypes such
// as edits. It deliberately preserves text byte-for-byte; gc sanitizes the
// prompt boundary after routing it to the configured named session.
export function normalizeSlackMessage(event, { channelID, scopeID, provider, accountID }) {
  if (!event || event.channel !== channelID || event.bot_id || event.subtype) return null
  if (typeof event.user !== 'string' || event.user === '' || typeof event.text !== 'string') return null

  const providerMessageID = event.client_msg_id || event.ts
  if (typeof providerMessageID !== 'string' || providerMessageID === '') return null

  const timestamp = Number(event.ts)
  const receivedAt = Number.isFinite(timestamp) ? new Date(timestamp * 1000) : new Date()
  return {
    provider_message_id: providerMessageID,
    conversation: {
      scope_id: scopeID,
      provider,
      account_id: accountID,
      conversation_id: channelID,
      kind: 'room',
    },
    actor: { id: event.user, display_name: event.user, is_bot: false },
    text: event.text,
    received_at: receivedAt.toISOString(),
  }
}

// makeSlackSocketHandler creates the Socket Mode callback. Slack requires an
// acknowledgement promptly, so ack runs before the (potentially retried) gc
// forward. Returning the forward promise retains serial ordering for callers.
export function makeSlackSocketHandler({ channelID, scopeID, provider, accountID, forward }) {
  let inboundChain = Promise.resolve()
  return async ({ event, ack }) => {
    await ack()
    const message = normalizeSlackMessage(event, { channelID, scopeID, provider, accountID })
    if (!message) return
    // Socket Mode has no replay cursor once an envelope is acknowledged. Keep
    // pending forwards ordered in memory and do not let one unexpected error
    // poison the queue behind it.
    inboundChain = inboundChain.catch(() => {}).then(() => forward(message))
    await inboundChain
  }
}

// makeSlackForward retries an already-acknowledged Socket Mode event until gc
// accepts its normalized message. Slack has no cursor to request a replay
// after acknowledgement, so this mirrors the iMessage bridge's in-process
// durability policy: keep retrying while alive and report a shutdown loss.
export function makeSlackForward({ gcFetch, isShuttingDown, sleep, log, onUndelivered }) {
  return async (message) => {
    const delivered = await forwardWithRetry({
      update: { update_id: message.provider_message_id, message },
      deliver: ({ message: normalized }) => gcFetch('POST', '/extmsg/inbound', { message: normalized }),
      maxAttempts: Infinity,
      isShuttingDown,
      sleep,
      log,
    })
    if (!delivered) onUndelivered?.()
    return delivered
  }
}
