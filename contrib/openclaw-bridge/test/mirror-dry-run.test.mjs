// Pins mirror-dry-run.mjs: what it counts, and the two ways its answer can be
// quietly wrong.
//
// THE ANSWER THIS TOOL PRODUCES IS USED TO DECIDE whether the mirror may be
// started against a channel people read, so both of its failure directions
// matter and they are not symmetric. Under-counting reads as "safe to start"
// and is the dangerous one; `a_snapshot_fed_mirror_publishes_nothing` is the
// case that pins it, because feeding the transcript the way the mirror
// watermarks it would report 0 for every input and look like good news.
//
// Over-counting reads as "keep it off" and merely wastes the decision;
// `only_final_assistant_text_is_counted` covers that direction.
//
//     node --test test/mirror-dry-run.test.mjs

import assert from 'node:assert/strict'
import test from 'node:test'
import { createAssistantTurnMirror } from '../lib/assistant-turn-mirror.mjs'
import { dryRun, summarize } from '../mirror-dry-run.mjs'

const at = (seconds) => new Date(Date.UTC(2026, 0, 1, 0, 0, seconds)).toISOString()

const assistant = (id, text, seconds) => ({
  id, role: 'assistant', status: 'final', timestamp: at(seconds),
  blocks: [{ type: 'text', text }],
})

test('only final assistant text is counted', async () => {
  const published = await dryRun([
    assistant('a', 'answered', 0),
    { id: 'b', role: 'user', status: 'final', timestamp: at(1), blocks: [{ type: 'text', text: 'asked' }] },
    { id: 'c', role: 'assistant', status: 'partial', timestamp: at(2), blocks: [{ type: 'text', text: 'mid' }] },
    { id: 'd', role: 'assistant', status: 'final', timestamp: at(3), blocks: [{ type: 'tool_use', name: 'Bash' }] },
  ])
  assert.equal(published.length, 1)
  assert.equal(published[0].timestamp, at(0))
})

test('a long turn counts as every chunk it would be split into', async () => {
  // The decision is about how many SLACK MESSAGES arrive, not how many turns
  // the agent took, so a turn past the limit must count more than once.
  const published = await dryRun([assistant('a', 'x'.repeat(200), 0)], 64)
  assert.ok(published.length > 1, 'a turn over the limit must count as several messages')
  assert.equal(new Set(published.map((p) => p.key)).size, published.length,
    'each chunk carries its own idempotency key')
})

test('a snapshot fed mirror publishes nothing, which is why dryRun sends upserts', async () => {
  // The trap, stated as a test rather than only as a comment in the tool. If
  // dryRun ever fed its input the way this case does, it would answer 0 for
  // every transcript and read as a clean bill of health.
  const messages = [assistant('a', 'answered', 0), assistant('b', 'again', 1)]
  const seen = []
  const mirror = createAssistantTurnMirror({
    conversation: { scope_id: 's', provider: 'slack', account_id: 'a', conversation_id: 'c', kind: 'room' },
    sessionID: 's',
    publish: async (body) => { seen.push(body) },
  })
  await mirror.handleStructuredEvent({ operation: 'snapshot', structured_messages: messages })
  assert.equal(seen.length, 0, 'a snapshot is a watermark')
  assert.equal((await dryRun(messages)).length, 2, 'dryRun must still report both turns')
})

test('the opening window and the densest window are different measurements', async () => {
  // A transcript that is quiet at its start and busy later. Collapsing the two
  // into one number is the plausible simplification, and it would have hidden
  // exactly the distinction this tool was written to make: the 92-message
  // incident was an OPENING-window event.
  const messages = [assistant('a', 'one', 0)]
  for (let i = 0; i < 5; i += 1) messages.push(assistant(`b${i}`, 'burst', 600 + i))
  const summary = summarize(await dryRun(messages), [45])
  assert.equal(summary.opening[45], 1)
  assert.equal(summary.densest[45], 5)
})

test('an empty transcript summarizes to zeros rather than throwing', () => {
  const summary = summarize([], [45])
  assert.equal(summary.total, 0)
  assert.equal(summary.spanSeconds, 0)
  assert.equal(summary.perHour, 0)
})

test('a single turn does not divide by a zero span', async () => {
  const summary = summarize(await dryRun([assistant('a', 'only', 0)]), [45])
  assert.equal(summary.total, 1)
  assert.ok(Number.isFinite(summary.perHour), 'perHour must be finite for an instantaneous span')
})
