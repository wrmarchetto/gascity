#!/usr/bin/env node
// Slack Socket Mode adapter for one configured channel. Socket Mode holds an
// outbound WebSocket, so the lab exposes no public Slack webhook endpoint.
// gc owns routing and prompt delivery; this process normalizes Slack events,
// registers its outbound callback, and binds the configured named-session
// identity to the configured channel.

import { SocketModeClient } from '@slack/socket-mode'
import { WebClient } from '@slack/web-api'
import { makeSlackForward, makeSlackSocketHandler } from './lib/slack.mjs'
import { env, makeAdapterRegistrar, makeGcClient, makeNamedSessionBinder, makeShutdown, startCallbackServer } from './lib/gc-client.mjs'

const required = (name) => {
  const value = process.env[name]
  if (!value) {
    console.error(`[slack-bridge] ${name} is required`)
    process.exit(2)
  }
  return value
}

const CITY = required('GC_CITY')
const APP_TOKEN = required('BRIDGE_SLACK_APP_TOKEN')
const BOT_TOKEN = required('BRIDGE_SLACK_BOT_TOKEN')
const CHANNEL_ID = required('BRIDGE_SLACK_CHANNEL_ID')
const TARGET_AGENT = required('SLACK_TARGET_AGENT')
const GC_BASE = env('GC_BASE_URL', 'http://127.0.0.1:8372')
const SCOPE = env('GC_SCOPE_ID', CITY)
const PROVIDER = env('BRIDGE_PROVIDER', 'slack')
const ACCOUNT = env('BRIDGE_ACCOUNT_ID', 'default')
const PORT = Number(env('BRIDGE_PORT', '8932'))

const redact = (value) => String(value).split(APP_TOKEN).join('<app-token>').split(BOT_TOKEN).join('<bot-token>')
const log = (...args) => console.log('[slack-bridge]', ...args.map(redact))
const logError = (...args) => console.error('[slack-bridge]', ...args.map(redact))
const conversation = {
  scope_id: SCOPE,
  provider: PROVIDER,
  account_id: ACCOUNT,
  conversation_id: CHANNEL_ID,
  kind: 'room',
}

const { gcFetch } = makeGcClient({ baseUrl: GC_BASE, city: CITY })
const web = new WebClient(BOT_TOKEN)
let shuttingDown = false
let undeliveredOnShutdown = 0
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

const forward = makeSlackForward({
  gcFetch,
  isShuttingDown: () => shuttingDown,
  sleep,
  log,
  onUndelivered: () => {
    undeliveredOnShutdown += 1
  },
})

const socket = new SocketModeClient({ appToken: APP_TOKEN })
socket.on(
  'message',
  makeSlackSocketHandler({
    channelID: CHANNEL_ID,
    scopeID: SCOPE,
    provider: PROVIDER,
    accountID: ACCOUNT,
    forward,
  }),
)
socket.on('error', (error) => logError('Socket Mode error:', error?.message ?? error))

async function handleRequest(req, rawBody) {
  if (req.method !== 'POST' || req.url !== '/publish') return { status: 404, body: { error: 'not found' } }
  const publish = JSON.parse(rawBody)
  try {
    const result = await web.chat.postMessage({
      channel: CHANNEL_ID,
      text: String(publish.text ?? ''),
      ...(publish.reply_to_message_id ? { thread_ts: publish.reply_to_message_id } : {}),
    })
    return {
      status: 200,
      body: { message_id: String(result.ts ?? ''), conversation: publish.conversation, delivered: true },
    }
  } catch (error) {
    logError('publish failed:', error?.message ?? error)
    return {
      status: 200,
      body: {
        conversation: publish.conversation,
        delivered: false,
        failure_kind: 'transient',
        metadata: { error: redact(error?.message ?? error) },
      },
    }
  }
}

const server = await startCallbackServer({ handleRequest, port: PORT })
const registrar = makeAdapterRegistrar({
  gcFetch,
  baseUrl: GC_BASE,
  provider: PROVIDER,
  account: ACCOUNT,
  name: 'slack-socket-mode-bridge',
  callbackUrl: `http://127.0.0.1:${PORT}`,
  capabilities: { SupportsChildConversations: false, SupportsAttachments: false, MaxMessageLength: 40000 },
  log,
})

await registrar.registerWithRetry()
await makeNamedSessionBinder({ gcFetch, conversation, agentName: TARGET_AGENT, log }).bindWithRetry()
await socket.start()
const reregister = registrar.startReregister()
log(`ready: channel=${CHANNEL_ID} session=${TARGET_AGENT} city=${CITY}`)

const shutdown = makeShutdown({
  log,
  reregister,
  unregister: registrar.unregister,
  server,
  teardown: async () => {
    shuttingDown = true
    await socket.disconnect().catch(() => {})
    if (undeliveredOnShutdown > 0) {
      logError(`${undeliveredOnShutdown} inbound Slack message(s) were not delivered before shutdown`)
    }
  },
  exitCode: () => (undeliveredOnShutdown > 0 ? 1 : 0),
})
process.on('SIGINT', shutdown)
process.on('SIGTERM', shutdown)
