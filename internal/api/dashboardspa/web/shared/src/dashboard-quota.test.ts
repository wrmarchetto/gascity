// Run with: npx tsx --test shared/src/dashboard-quota.test.ts
//
// Scope: the account-quota interpretation rules -- how a raw reading becomes
// something safe to render. This suite exists because every rule here guards
// against a confident-looking wrong number, and none of them can be exercised
// from the Go side: the classification runs against the BROWSER's clock at
// render time, which no server-side test has. Grouping and formatting are
// delegated to the route (Accounts.test.tsx) and to hooks/time.ts.
//
// Fixture ages are computed FROM the 15-minute threshold rather than written as
// convenient timestamps. A suite that picks its own timestamps drifts away from
// the constant it means to pin, and then passes whatever the constant becomes.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  QUOTA_STALE_AFTER_SECONDS,
  classifyQuotaReading,
  formatQuotaPercentage,
  groupAccountsForDisplay,
  type AccountQuotaEntry,
  type AccountQuotaReport,
} from './dashboard-quota.js';

const NOW = 1_800_000_000;
// Every age below is expressed against the threshold, so moving
// QUOTA_STALE_AFTER_SECONDS moves the fixtures with it.
const JUST_INSIDE = QUOTA_STALE_AFTER_SECONDS - 1;
const JUST_OUTSIDE = QUOTA_STALE_AFTER_SECONDS + 1;
const FUTURE_RESET = NOW + 3_600;

function window(percentage: number, resetsAt = FUTURE_RESET) {
  return { used_percentage: percentage, resets_at: resetsAt };
}

test('the staleness threshold is the operator-specified 15 minutes', () => {
  // Written as a bare literal on purpose. Every other test here derives its
  // fixture ages FROM the constant, and C-1 < C < C+1 holds for every C, so
  // the relational tests pass just as happily with a 1-second or 15-hour
  // threshold -- measured, by setting the constant to 1 and watching all
  // eleven stay green. This is the only assertion that pins the value the
  // operator actually asked for.
  assert.equal(QUOTA_STALE_AFTER_SECONDS, 900);
});

test('a reading younger than the threshold is current, and carries its age', () => {
  const reading = classifyQuotaReading(NOW - JUST_INSIDE, window(62), NOW);
  assert.equal(reading.state, 'current');
  assert.equal(reading.percentage, 62);
  assert.equal(reading.ageSeconds, JUST_INSIDE);
});

test('a reading older than the threshold is stale but still shows its value', () => {
  const reading = classifyQuotaReading(NOW - JUST_OUTSIDE, window(62), NOW);
  assert.equal(reading.state, 'stale');
  // Explicitly NOT null. The operator's call is to show the number with its
  // age and gray it -- hiding it and silently refreshing it are both wrong.
  assert.equal(reading.percentage, 62);
  assert.equal(reading.ageSeconds, JUST_OUTSIDE);
});

test('the threshold itself is not yet stale, so the boundary has one owner', () => {
  const reading = classifyQuotaReading(NOW - QUOTA_STALE_AFTER_SECONDS, window(62), NOW);
  assert.equal(reading.state, 'current');
});

test('a rolled window withholds the percentage however fresh the reading is', () => {
  // A reading taken one second ago whose window ends this second: the
  // percentage describes a window that no longer exists. Freshness cannot
  // rescue it, which is why the rolled check runs before the age check.
  const reading = classifyQuotaReading(NOW - 1, window(62, NOW), NOW);
  assert.equal(reading.state, 'rolled');
  assert.equal(reading.percentage, null);
  assert.equal(reading.resetsAt, NOW);
});

test('a stale reading whose window rolled while nothing watched withholds it too', () => {
  // The real idle-account case: nothing has run on the account for hours, so
  // the last reading is both stale AND describes an expired window. Rolled
  // wins -- a stale-but-plausible percentage shown confidently is the exact
  // failure this tab exists to prevent.
  const reading = classifyQuotaReading(NOW - 4 * 3_600, window(62, NOW - 3_600), NOW);
  assert.equal(reading.state, 'rolled');
  assert.equal(reading.percentage, null);
});

test('a record written after its own window ended withholds it', () => {
  // Degenerate but observable: a collector that wrote a reading whose window
  // had already closed. Nothing about it is trustworthy.
  const observedAt = NOW - 60;
  const reading = classifyQuotaReading(observedAt, window(62, observedAt - 1), NOW);
  assert.equal(reading.state, 'rolled');
  assert.equal(reading.percentage, null);
});

test('a window ending before a future-dated reading rolls rather than clamping', () => {
  // Pins the SECOND clause of the rolled check, resets_at < observed_at. Every
  // other rolled fixture also satisfies resets_at <= now, so the first clause
  // returns early and the second is deletable with the suite green -- measured.
  // Only a future observed_at separates them: this reading claims to come from
  // five minutes ahead of the browser, and its window closed 200s before that,
  // so the window is over even though resets_at is still ahead of the browser.
  const reading = classifyQuotaReading(NOW + 300, window(62, NOW + 100), NOW);
  assert.equal(reading.state, 'rolled');
  assert.equal(reading.percentage, null);
});

test('a percentage renders without the collector arithmetic noise', () => {
  // Not a synthetic case: account 5 on this host reported
  // 14.000000000000002 for its 7d window, which reaches the wire verbatim and
  // renders as "14.000000000000002%". The clean 62.0 / 18.0 values in every
  // fixture above could not surface it.
  assert.equal(formatQuotaPercentage(14.000000000000002), '14');
  // A genuine half must survive. Rounding to whole percent would misreport a
  // level the operator may be watching against a cap.
  assert.equal(formatQuotaPercentage(62.5), '62.5');
  assert.equal(formatQuotaPercentage(33.333333), '33.3');
  assert.equal(formatQuotaPercentage(100), '100');
  assert.equal(formatQuotaPercentage(0), '0');
});

test('a future observed_at clamps to zero age instead of rendering a negative one', () => {
  // Clock skew between the collector host and the browser. "as of -3m ago"
  // reads as a bug in the tab rather than a bug in the clocks.
  const reading = classifyQuotaReading(NOW + 180, window(62), NOW);
  assert.equal(reading.ageSeconds, 0);
  assert.equal(reading.state, 'current');
});

function entry(account: string, overrides: Partial<AccountQuotaEntry> = {}): AccountQuotaEntry {
  return {
    account,
    label: '',
    in_pool: false,
    observation: {
      state: 'never_observed',
      observed_at: null,
      session_id: '',
      five_hour: null,
      seven_day: null,
      reason: '',
    },
    last_used_at: null,
    cooldown_until: null,
    bound_sessions: 0,
    suspect_sessions: 0,
    ...overrides,
  };
}

function report(accounts: AccountQuotaEntry[], pool: string[]): AccountQuotaReport {
  return {
    accounts,
    pool,
    rotation: { available: true, reason: '' },
    homes_dir: '/home/op/.claude-homes',
    unattributed_suspects: 0,
  };
}

test('grouping separates the rotation pool from the accounts held out of it', () => {
  const groups = groupAccountsForDisplay(
    report(
      [
        entry('0', { label: 'operator interactive' }),
        entry('1', { label: 'orchestrator pin' }),
        entry('2', { in_pool: true }),
        entry('3', { in_pool: true }),
      ],
      ['2', '3'],
    ),
  );

  // Three distinct groups, so a reader cannot come away thinking the rotation
  // has four accounts.
  assert.deepEqual(
    groups.map((g) => g.label),
    ['Rotation pool', 'operator interactive', 'orchestrator pin'],
  );
  assert.deepEqual(
    groups[0]?.accounts.map((a) => a.account),
    ['2', '3'],
  );
  assert.equal(groups[0]?.inPool, true);
});

test('grouping follows in_pool, never the account id', () => {
  // Every other grouping fixture pools accounts "2" and "3", which is what the
  // rotation happens to hold today. That makes them blind to an implementation
  // keying off the id: replacing the in_pool test with
  // ['2','3'].includes(account.account) passed this whole file and the route
  // suite -- measured. Here the pooled account is 0 and 2 is held out, an
  // arrangement no baked-in list gets right, so only reading in_pool works.
  const groups = groupAccountsForDisplay(
    report([entry('0', { in_pool: true }), entry('2', { label: 'held' })], ['0']),
  );
  assert.deepEqual(
    groups.map((g) => g.label),
    ['Rotation pool', 'held'],
  );
  assert.deepEqual(
    groups[0]?.accounts.map((a) => a.account),
    ['0'],
  );
});

test('an unlabeled out-of-pool account is named honestly, not guessed at', () => {
  const groups = groupAccountsForDisplay(
    report([entry('0'), entry('2', { in_pool: true })], ['2']),
  );
  assert.deepEqual(
    groups.map((g) => g.label),
    ['Rotation pool', 'Not in rotation'],
  );
});

test('the pool group stays present but empty when every account is held out', () => {
  // Otherwise a pool that has been emptied looks identical to a dashboard with
  // no pool concept, and a capacity incident reads as "nothing to see".
  const groups = groupAccountsForDisplay(report([entry('0', { label: 'operator' })], []));
  assert.equal(groups[0]?.label, 'Rotation pool');
  assert.deepEqual(groups[0]?.accounts, []);
});

test('accounts sharing a label collect into one group', () => {
  const groups = groupAccountsForDisplay(
    report([entry('0', { label: 'reserved' }), entry('1', { label: 'reserved' })], []),
  );
  assert.equal(groups.length, 2);
  assert.deepEqual(
    groups[1]?.accounts.map((a) => a.account),
    ['0', '1'],
  );
});
