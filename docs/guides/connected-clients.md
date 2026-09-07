---
title: Connect an external messaging adapter
description: Connect an out-of-process messaging adapter so a city can receive and publish messages on an external service.
---

An **external messaging adapter** is a service you run beside Gas City. It
translates an external service's events into normalized messages and receives
published messages from a city.

## Overview

An adapter connects to one city through three API calls:

1. Register its callback with `POST /v0/city/{city}/extmsg/adapters`.
2. Send normalized external messages to `POST /v0/city/{city}/extmsg/inbound`.
3. Ask Gas City to publish a session message with `POST /v0/city/{city}/extmsg/outbound`.

For an introduction to the roles of Agents, Beads, Formulas, Rigs, Packs, and
Events, see [How Gas City works](/getting-started/how-gas-city-works).

## Before you start

- Run an adapter HTTP service that can receive `POST /publish` requests.
- Choose a stable `provider` and `account_id` for the external account.
- Know the city name and its orchestrator HTTP address. The examples use
  `http://localhost:7375` and the city name `acme`.

The adapter is out-of-process. It owns external-service authentication,
webhooks, event verification, and conversion into normalized messages.

## Register the adapter

Register the adapter's callback base URL. Gas City appends `/publish` when it
delivers an outbound message, so do not include that suffix in `callback_url`.

```http
POST /v0/city/{city}/extmsg/adapters
Content-Type: application/json
X-GC-Request: 1

{
  "provider": "chat-service",
  "account_id": "acme-workspace",
  "name": "Acme chat adapter",
  "callback_url": "http://adapter.internal:8080"
}
```

The response confirms the registered provider and account:

```json
{
  "status": "registered",
  "provider": "chat-service",
  "account_id": "acme-workspace",
  "name": "Acme chat adapter"
}
```

## Receive outbound messages

When a city sends a message to an external conversation, it calls
`POST /publish` on the callback URL you registered. Implement that endpoint in
your adapter. It receives the originating session, the conversation identity,
and the text to publish:

```json
{
  "session_id": "session-123",
  "conversation": {
    "scope_id": "workspace-42",
    "provider": "chat-service",
    "account_id": "acme-workspace",
    "conversation_id": "channel-7",
    "kind": "room"
  },
  "text": "Deployment is complete."
}
```

After publishing to the external service, return a receipt. A successful
receipt includes the conversation and sets `delivered` to `true`:

```json
{
  "message_id": "external-message-99",
  "conversation": {
    "scope_id": "workspace-42",
    "provider": "chat-service",
    "account_id": "acme-workspace",
    "conversation_id": "channel-7",
    "kind": "room"
  },
  "delivered": true
}
```

## Submit inbound messages

Verify the external webhook in your adapter, normalize it, then send the
normalized message to Gas City:

```http
POST /v0/city/{city}/extmsg/inbound
Content-Type: application/json
X-GC-Request: 1

{
  "message": {
    "provider_message_id": "external-message-100",
    "conversation": {
      "scope_id": "workspace-42",
      "provider": "chat-service",
      "account_id": "acme-workspace",
      "conversation_id": "channel-7",
      "kind": "room"
    },
    "actor": {
      "id": "user-7",
      "display_name": "Ari"
    },
    "text": "What changed in this deployment?",
    "received_at": "2026-09-07T17:00:00Z"
  }
}
```

The registered HTTP adapter does not send raw external webhooks to Gas City.
It is responsible for verification and normalization before this request.

## Ask Gas City to publish

Use the outbound endpoint to publish a message from a session through the
registered adapter. Gas City finds the adapter from the conversation's
`provider` and `account_id`, then forwards the message to its `/publish`
endpoint.

```http
POST /v0/city/{city}/extmsg/outbound
Content-Type: application/json
X-GC-Request: 1

{
  "session_id": "session-123",
  "conversation": {
    "scope_id": "workspace-42",
    "provider": "chat-service",
    "account_id": "acme-workspace",
    "conversation_id": "channel-7",
    "kind": "room"
  },
  "text": "Deployment is complete."
}
```

## Restart and reconnect

The adapter registry is in memory. Registrations disappear when the
orchestrator restarts, so an out-of-process adapter must register again after
it reconnects. Until it does, inbound and outbound delivery for that provider
and account cannot use the adapter.

## Next steps

Use [the API reference](/reference/api) for the broader control-plane surface.
