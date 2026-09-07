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

// createAssistantTurnMirror returns a stateful SSE-frame consumer. Stable
// structured message IDs make snapshot/upsert replay harmless during one daemon
// lifetime. The lifecycle component owns process restart persistence.
export function createAssistantTurnMirror({ conversation, sessionID, publish, maxMessageLength = defaultMaxMessageLength }) {
  if (!conversation || typeof conversation !== 'object') throw new TypeError('conversation is required')
  if (typeof sessionID !== 'string' || sessionID === '') throw new TypeError('sessionID is required')
  if (typeof publish !== 'function') throw new TypeError('publish is required')
  const publishedIDs = new Set()

  async function handleStructuredEvent(event) {
    const messages = event?.structured_messages
    if (!Array.isArray(messages)) return
    for (const message of messages) {
      const id = typeof message?.id === 'string' ? message.id : ''
      const text = finalAssistantText(message)
      if (id === '' || text === '' || publishedIDs.has(id)) continue
      const chunks = chunkAssistantTurn(text, maxMessageLength)
      for (const [index, chunk] of chunks.entries()) {
        await publish({
          session_id: sessionID,
          conversation,
          text: chunk,
          idempotency_key: `assistant-turn:${id}:${index + 1}`,
        })
      }
      publishedIDs.add(id)
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
