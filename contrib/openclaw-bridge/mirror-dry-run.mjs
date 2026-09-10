#!/usr/bin/env node
// Report what slack-mirror.mjs WOULD publish for a recorded session
// transcript, without sending anything to Slack or to gc.
//
// WHY THIS EXISTS. The mirror was held out of service after it put 92 messages
// into a Slack channel in 45 seconds on one cold wake. Deciding whether it is
// safe to start needs a number, and the only two ways to get one are to start
// it and watch a real channel, or to replay a real transcript through the real
// selection code. This is the second. It imports createAssistantTurnMirror
// rather than reimplementing the eligibility rule, because a dry run that
// disagreed with the mirror about what counts as a turn would answer a
// question nobody asked.
//
// It reports the OPENING window separately from the densest one. Those measure
// different claims and the distinction is the whole point here: "does a cold
// wake burst" is a question about the first seconds of a transcript, while
// "is the steady rate tolerable" is a question about the worst window
// anywhere. The 92-message incident was an opening-window event.
//
// INPUT is a JSON array of structured messages, which is what the
// `structured_messages` field of a session's structured stream carries:
//
//   curl -sN -H 'X-GC-Request: dry-run' \
//     "$GC_BASE/v0/city/$CITY/session/$SESSION/stream?format=structured" \
//     | sed -n 's/^data: //p' | head -1 \
//     | python3 -c 'import json,sys; json.dump(json.load(sys.stdin)["structured_messages"], sys.stdout)' \
//     > transcript.json
//
// A stream asked with no resume cursor answers with the whole transcript in
// one snapshot frame, which is exactly the recording this wants.
//
// EACH MESSAGE IS FED AS ITS OWN upsert, which is how a live transcript grows.
// Feeding the snapshot as a single frame would select the same set -- ids are
// settled once either way -- but the mirror WATERMARKS a snapshot and would
// publish nothing, so a dry run written that way reports 0 for every input and
// looks like good news. That is the trap this file is most likely to be
// rewritten into.
//
// Verified by test/mirror-dry-run.test.mjs.

import { readFileSync } from 'node:fs'
import { createAssistantTurnMirror, defaultMaxMessageLength } from './lib/assistant-turn-mirror.mjs'

// dryRun returns the messages the mirror would publish, each tagged with the
// timestamp of the transcript entry that produced it.
export async function dryRun(messages, maxMessageLength = defaultMaxMessageLength) {
  const published = []
  let current = null
  const mirror = createAssistantTurnMirror({
    conversation: { scope_id: 'dry-run', provider: 'slack', account_id: 'dry-run', conversation_id: 'dry-run', kind: 'room' },
    sessionID: 'dry-run',
    publish: async (body) => {
      published.push({ timestamp: current?.timestamp ?? '', length: body.text.length, key: body.idempotency_key })
    },
    maxMessageLength,
  })
  for (const message of messages) {
    current = message
    await mirror.handleStructuredEvent({ operation: 'upsert', structured_messages: [message] })
  }
  return published
}

// summarize turns a dry run into the figures a start/do-not-start decision
// needs. Windows are in seconds. An empty run reports zeros rather than
// throwing: a transcript with no assistant text is a real answer.
export function summarize(published, windows = [45, 60, 300, 3600]) {
  const times = published.map((p) => Date.parse(p.timestamp)).filter(Number.isFinite).sort((a, b) => a - b)
  const lengths = published.map((p) => p.length).sort((a, b) => a - b)
  if (times.length === 0) return { total: published.length, spanSeconds: 0, perHour: 0, lengths: {}, densest: {}, opening: {} }
  const spanSeconds = (times.at(-1) - times[0]) / 1000
  const at = (fraction) => lengths[Math.min(lengths.length - 1, Math.floor(lengths.length * fraction))]
  const densest = {}
  const opening = {}
  for (const window of windows) {
    let best = 0
    for (let i = 0; i < times.length; i += 1) {
      let j = i
      while (j < times.length && times[j] - times[i] <= window * 1000) j += 1
      if (j - i > best) best = j - i
    }
    densest[window] = best
    opening[window] = times.filter((t) => t - times[0] <= window * 1000).length
  }
  return {
    total: published.length,
    spanSeconds,
    // A span of zero would divide by zero; one message in an instant is
    // reported as one per hour rather than as infinity.
    perHour: spanSeconds > 0 ? (published.length / (spanSeconds / 3600)) : published.length,
    lengths: { min: lengths[0], median: at(0.5), p90: at(0.9), max: lengths.at(-1), underTwoHundred: lengths.filter((l) => l < 200).length },
    densest,
    opening,
  }
}

async function main() {
  const path = process.argv[2]
  if (!path) {
    console.error('usage: mirror-dry-run.mjs <structured-messages.json>')
    process.exit(2)
  }
  const messages = JSON.parse(readFileSync(path, 'utf8'))
  if (!Array.isArray(messages)) {
    console.error('input must be a JSON array of structured messages')
    process.exit(2)
  }
  const summary = summarize(await dryRun(messages))
  console.log(`transcript entries        ${messages.length}`)
  console.log(`would publish             ${summary.total} messages`)
  console.log(`span                      ${(summary.spanSeconds / 3600).toFixed(2)} h`)
  console.log(`average rate              ${summary.perHour.toFixed(1)} messages/hour`)
  if (summary.total > 0) {
    const l = summary.lengths
    console.log(`length min/med/p90/max    ${l.min} / ${l.median} / ${l.p90} / ${l.max}`)
    console.log(`under 200 chars           ${l.underTwoHundred} of ${summary.total}`)
  }
  for (const window of Object.keys(summary.densest)) {
    console.log(`densest ${String(window).padStart(4)}s window     ${summary.densest[window]}`)
  }
  for (const window of Object.keys(summary.opening)) {
    console.log(`opening ${String(window).padStart(4)}s            ${summary.opening[window]}`)
  }
}

if (import.meta.url === `file://${process.argv[1]}`) await main()
