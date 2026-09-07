// Entrypoint smoke coverage for the two executable bridges. bridge.mjs and
// telegram-bridge.mjs have top-level side effects (env checks, daemon probes,
// callback servers), so they cannot be imported in-process without starting
// real I/O. `node --check` parses each file without executing it, which catches
// syntax errors and other parse-time breakage in the executable entrypoints
// that the lib-only suites would otherwise let merge. The openclaw loader and
// shared-lib imports the entrypoints depend on are covered separately by
// openclaw-loader.test.mjs, gc-client.test.mjs, and inbound.test.mjs.
//
// Run: npm test  (or: node --test test/)

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)
const entrypoint = (f) => fileURLToPath(new URL(`../${f}`, import.meta.url))

for (const file of ['bridge.mjs', 'telegram-bridge.mjs', 'slack-bridge.mjs', 'slack-mirror.mjs']) {
  test(`node --check passes for ${file} (executable entrypoint parses)`, async () => {
    try {
      await execFileAsync(process.execPath, ['--check', entrypoint(file)])
    } catch (err) {
      assert.fail(`node --check ${file} failed:\n${err.stderr || err.message}`)
    }
  })
}

test('slack bridge refuses to start when the bot token is absent', async () => {
  const env = {
    ...process.env,
    GC_CITY: 'lab',
    BRIDGE_SLACK_APP_TOKEN: 'xapp-test',
    BRIDGE_SLACK_BOT_TOKEN: '',
    BRIDGE_SLACK_CHANNEL_ID: 'C012345',
    SLACK_TARGET_AGENT: 'lab/lead',
  }
  try {
    await execFileAsync(process.execPath, [entrypoint('slack-bridge.mjs')], { env })
    assert.fail('slack bridge started without BRIDGE_SLACK_BOT_TOKEN')
  } catch (err) {
    assert.equal(err.code, 2)
    assert.match(err.stderr, /BRIDGE_SLACK_BOT_TOKEN is required/)
  }
})

test('slack bridge refuses to start when the Socket Mode app token is absent', async () => {
  const env = {
    ...process.env,
    GC_CITY: 'lab',
    BRIDGE_SLACK_APP_TOKEN: '',
    BRIDGE_SLACK_BOT_TOKEN: 'xoxb-test',
    BRIDGE_SLACK_CHANNEL_ID: 'C012345',
    SLACK_TARGET_AGENT: 'lab/lead',
  }
  try {
    await execFileAsync(process.execPath, [entrypoint('slack-bridge.mjs')], { env })
    assert.fail('slack bridge started without BRIDGE_SLACK_APP_TOKEN')
  } catch (err) {
    assert.equal(err.code, 2)
    assert.match(err.stderr, /BRIDGE_SLACK_APP_TOKEN is required/)
  }
})
