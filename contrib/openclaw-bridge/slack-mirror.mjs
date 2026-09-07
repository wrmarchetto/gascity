#!/usr/bin/env node
// Consume one configured session's structured transcript stream and publish its
// final assistant text to the configured Slack conversation through gc's extmsg
// outbound API. Supervision/restart ownership deliberately stays outside this
// process so it can be composed with the adapter lifecycle independently.

import { createAssistantTurnMirror, streamAssistantTurns } from './lib/assistant-turn-mirror.mjs'
import { env, makeGcClient } from './lib/gc-client.mjs'

const required = (name) => {
  const value = process.env[name]
  if (!value) {
    console.error(`[slack-mirror] ${name} is required`)
    process.exit(2)
  }
  return value
}

const CITY = required('GC_CITY')
const SESSION = required('GC_MIRROR_SESSION')
const CHANNEL_ID = required('SLACK_CHANNEL_ID')
const GC_BASE = env('GC_BASE_URL', 'http://127.0.0.1:8372')
const SCOPE = env('GC_SCOPE_ID', CITY)
const PROVIDER = env('BRIDGE_PROVIDER', 'slack')
const ACCOUNT = env('BRIDGE_ACCOUNT_ID', 'default')
const MAX_MESSAGE_LENGTH = Number(env('SLACK_MIRROR_MAX_MESSAGE_LENGTH', '39000'))

const conversation = {
  scope_id: SCOPE,
  provider: PROVIDER,
  account_id: ACCOUNT,
  conversation_id: CHANNEL_ID,
  kind: 'room',
}
const { gcFetch } = makeGcClient({ baseUrl: GC_BASE, city: CITY })
const mirror = createAssistantTurnMirror({
  conversation,
  sessionID: SESSION,
  maxMessageLength: MAX_MESSAGE_LENGTH,
  publish: (body) => gcFetch('POST', '/extmsg/outbound', body),
})

const controller = new AbortController()
const stop = () => controller.abort()
process.on('SIGINT', stop)
process.on('SIGTERM', stop)

const streamURL = new URL(`/v0/city/${encodeURIComponent(CITY)}/session/${encodeURIComponent(SESSION)}/stream`, GC_BASE)
streamURL.searchParams.set('format', 'structured')
const response = await fetch(streamURL, {
  headers: { Accept: 'text/event-stream' },
  signal: controller.signal,
})
if (!response.ok) throw new Error(`GET ${streamURL.pathname}: HTTP ${response.status}`)
await streamAssistantTurns({ response, onStructuredEvent: mirror.handleStructuredEvent })
