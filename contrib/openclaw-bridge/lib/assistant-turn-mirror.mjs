// The assistant-turn mirror is deliberately a consumer of the provider-neutral
// structured session stream. Its eligibility rule is exact: only a final
// structured message whose role is "assistant" contributes text; tool blocks,
// tool-result messages, and every user message are ignored.

// Slack accepts up to 40,000 UTF-16 code units. We reserve 1,000 code units so
// every long turn is split into lossless 39,000-unit parts labelled
// "[part i/n]". A reader sees both the ordering and that the turn continues;
// short turns remain unlabelled. Chunks never split a Unicode code point.
export const defaultMaxMessageLength = 39000

function finalAssistantText(message) {
  if (!message || message.role !== 'assistant' || message.status !== 'final' || !Array.isArray(message.blocks)) return ''
  return message.blocks
    .filter((block) => block?.type === 'text' && typeof block.text === 'string')
    .map((block) => block.text)
    .join('')
}

function splitWithPartCount(text, maxMessageLength, partCount) {
  const chars = Array.from(text)
  const chunks = []
  let start = 0
  for (let part = 1; start < chars.length; part += 1) {
    const prefix = `[part ${part}/${partCount}]\n`
    const capacity = maxMessageLength - prefix.length
    if (capacity < 1) throw new Error(`maxMessageLength ${maxMessageLength} is too small for mirror chunk labels`)
    let end = start
    let used = 0
    while (end < chars.length && used + chars[end].length <= capacity) {
      used += chars[end].length
      end += 1
    }
    if (end === start) throw new Error(`a Unicode code point exceeds the mirror chunk capacity ${capacity}`)
    chunks.push(prefix + chars.slice(start, end).join(''))
    start = end
  }
  return chunks
}

// chunkAssistantTurn splits a text turn without losing or truncating content.
// It iterates because the digit-width of the final part count changes the
// printable prefix capacity.
export function chunkAssistantTurn(text, maxMessageLength = defaultMaxMessageLength) {
  if (typeof text !== 'string') throw new TypeError('assistant turn text must be a string')
  if (!Number.isInteger(maxMessageLength) || maxMessageLength < 16) {
    throw new RangeError('maxMessageLength must be an integer of at least 16')
  }
  if (text.length <= maxMessageLength) return [text]

  let partCount = 2
  for (;;) {
    const chunks = splitWithPartCount(text, maxMessageLength, partCount)
    if (chunks.length === partCount) return chunks
    partCount = chunks.length
  }
}

// createAssistantTurnMirror returns a stateful SSE-frame consumer.
//
// A RESTATED TRANSCRIPT IS A WATERMARK, NOT CONTENT. The stream answers a
// request carrying no resume cursor with the target's WHOLE transcript in one
// frame (the empty-token branch of buildStructuredStreamUpdate,
// internal/api/session_structured_stream.go), and this consumer sends none.
// Every turn in such a frame is recorded as settled and never published.
// Measured 2026-09-08 on one start against a two-hour-old transcript: 91 turns
// posted in 43s, 2.1/s against a ~1/s per-channel ceiling, four rate-limit
// retries.
//
// EVERY RESTATEMENT IS THE SAME FRAME BY ANOTHER NAME, so the rule is keyed on
// the operation and not on which frame came first. operation:"upsert" is the
// only one that EXTENDS the transcript; a reconnect opens a new connection and
// so begins with another full snapshot, and a cursor the server cannot honor is
// answered with operation:"reset" carrying the whole projection again. Both
// restate under ids the consumer may never have settled -- a resumed target
// writes a fresh transcript file, so its entry ids are new even though its
// content is old -- and nothing but the operation separates that from a
// transcript that genuinely grew.
//
// AN UNRECOGNIZED OPERATION WATERMARKS. The field is required and enum-
// constrained on the wire, so this is a wire change rather than a normal state;
// between the two failures it chooses silence, which an operator notices and
// can recover, over a flood that rate-limits a channel shared with other
// publishers.
//
// THE REJECTED ALTERNATIVE is a persisted resume cursor passed as after_cursor
// on every connect, which is what a future editor reaches for and what the
// stream is built to accept. It is worth having for continuity but does NOT
// replace the watermark: the cases that invalidate a cursor are answered with a
// reset, so the flood returns by that door. Deferred, and only additive.
//
// WHAT IS DELIBERATELY GIVEN UP: a turn completed while the consumer was not
// reading the stream -- process down, or connection dropped -- is never
// delivered. Publishing it would mean telling a turn the consumer missed from
// one that merely predates it, and a restating frame expresses neither. The
// rejected softer rule, publishing the unsettled tail of a RECONNECT snapshot
// while watermarking only the first, buys those turns and reopens the flood:
// the reconnect that follows a respawn restates a whole transcript under ids
// that are new because the file is new.
export function createAssistantTurnMirror({ conversation, sessionID, publish, maxMessageLength = defaultMaxMessageLength }) {
  if (!conversation || typeof conversation !== 'object') throw new TypeError('conversation is required')
  if (typeof sessionID !== 'string' || sessionID === '') throw new TypeError('sessionID is required')
  if (typeof publish !== 'function') throw new TypeError('publish is required')
  // Settled: delivered, or watermarked as predating this process. Pending:
  // accepted for delivery and not yet acknowledged, which is the one thing a
  // restating frame must still publish -- otherwise a refused post becomes
  // silent loss the moment the stream reconnects.
  const settledIDs = new Set()
  const pendingIDs = new Set()

  async function handleStructuredEvent(event) {
    const messages = event?.structured_messages
    if (!Array.isArray(messages)) return
    const restatesTranscript = event?.operation !== 'upsert'
    for (const message of messages) {
      const id = typeof message?.id === 'string' ? message.id : ''
      const text = finalAssistantText(message)
      if (id === '' || text === '' || settledIDs.has(id)) continue
      if (restatesTranscript && !pendingIDs.has(id)) {
        settledIDs.add(id)
        continue
      }
      pendingIDs.add(id)
      const chunks = chunkAssistantTurn(text, maxMessageLength)
      for (const [index, chunk] of chunks.entries()) {
        await publish({
          session_id: sessionID,
          conversation,
          text: chunk,
          idempotency_key: `assistant-turn:${id}:${index + 1}`,
        })
      }
      pendingIDs.delete(id)
      settledIDs.add(id)
    }
  }

  return { handleStructuredEvent }
}

function parseSSEFrame(frame) {
  let event = 'message'
  const data = []
  for (const line of frame.split(/\r?\n/)) {
    if (line === '' || line.startsWith(':')) continue
    const separator = line.indexOf(':')
    const field = separator < 0 ? line : line.slice(0, separator)
    const value = separator < 0 ? '' : line.slice(separator + 1).replace(/^ /, '')
    if (field === 'event') event = value
    if (field === 'data') data.push(value)
  }
  return { event, data: data.join('\n') }
}

// streamAssistantTurns consumes only "structured" SSE frames. The API's Huma
// stream serializes one typed JSON event per frame; comments and activity frames
// are intentionally ignored.
export async function streamAssistantTurns({ response, onStructuredEvent }) {
  if (!response?.body) throw new Error('session stream response has no body')
  if (typeof onStructuredEvent !== 'function') throw new TypeError('onStructuredEvent is required')
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  for (;;) {
    const { done, value } = await reader.read()
    buffer += decoder.decode(value, { stream: !done })
    const frames = buffer.split(/\r?\n\r?\n/)
    buffer = frames.pop()
    for (const rawFrame of frames) {
      const frame = parseSSEFrame(rawFrame)
      if (frame.event !== 'structured' || frame.data === '') continue
      await onStructuredEvent(JSON.parse(frame.data))
    }
    if (done) break
  }
  if (buffer !== '') {
    const frame = parseSSEFrame(buffer)
    if (frame.event === 'structured' && frame.data !== '') await onStructuredEvent(JSON.parse(frame.data))
  }
}

function waitForReconnect(signal) {
  return new Promise((resolve) => {
    if (signal?.aborted) {
      resolve()
      return
    }
    const timer = setTimeout(done, 1000)
    function done() {
      clearTimeout(timer)
      signal?.removeEventListener('abort', done)
      resolve()
    }
    signal?.addEventListener('abort', done, { once: true })
  })
}

// reconnectAssistantTurnStream follows one stable session target across stream
// closure. The target is intentionally supplied to openStream on every attempt:
// a configured named session resolves to its current backing session after a
// respawn instead of preserving the previous volatile session bead ID.
export async function reconnectAssistantTurnStream({ sessionTarget, openStream, onStructuredEvent, signal, waitForReconnect: wait = waitForReconnect, onError, onReconnect }) {
  if (typeof sessionTarget !== 'string' || sessionTarget === '') throw new TypeError('sessionTarget is required')
  if (typeof openStream !== 'function') throw new TypeError('openStream is required')
  if (typeof onStructuredEvent !== 'function') throw new TypeError('onStructuredEvent is required')

  while (!signal?.aborted) {
    try {
      const response = await openStream(sessionTarget)
      await streamAssistantTurns({ response, onStructuredEvent })
    } catch (error) {
      if (signal?.aborted) return
      onError?.(error)
    }
    if (signal?.aborted) return
    onReconnect?.()
    await wait(signal)
  }
}
