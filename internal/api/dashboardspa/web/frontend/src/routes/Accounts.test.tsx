// Scope: what the Accounts tab actually RENDERS for each state a quota record
// can be in. The classification rules themselves are pinned in
// shared/src/dashboard-quota.test.ts; this suite exists because a correct
// classifier can still reach the screen as a confident-looking number if the
// route renders the wrong branch, and because the three group labels are the
// thing that stops a reader concluding the rotation has six accounts.
//
// Fixture timestamps are derived from the REAL clock at setup time and offset
// by ages computed from QUOTA_STALE_AFTER_SECONDS, so the boundary cases stay
// boundary cases if the threshold moves and no fixture can pass by matching a
// hardcoded date. Fake timers are deliberately NOT used: NowProvider ticks on a
// real interval and testing-library's async matchers wait on real timers, so a
// frozen clock deadlocks every findBy in this file.
//
//   npm --workspace gas-city-dashboard-frontend run test -- Accounts

import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { QUOTA_STALE_AFTER_SECONDS, type AccountQuotaReport } from 'gas-city-dashboard-shared';
import { AccountsPage } from './Accounts';
import { invalidate } from '../api/cache';
import { NowProvider } from '../contexts/NowContext';

let NOW_SECONDS = 0;
const JUST_INSIDE = QUOTA_STALE_AFTER_SECONDS - 60;
const WELL_OUTSIDE = QUOTA_STALE_AFTER_SECONDS + 600;

let currentReport: AccountQuotaReport = emptyReport();
let fetchMode: 'ok' | 'fail' = 'ok';

function emptyReport(): AccountQuotaReport {
  return {
    accounts: [],
    pool: [],
    rotation: { available: true, reason: '' },
    homes_dir: '/home/op/.claude-homes',
    unattributed_suspects: 0,
  };
}

type EntryOverrides = Partial<AccountQuotaReport['accounts'][number]>;

function entry(account: string, overrides: EntryOverrides = {}) {
  return {
    account,
    label: '',
    in_pool: false,
    observation: {
      state: 'never_observed' as const,
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

// A record observed `ageSeconds` ago whose windows are both still open, so only
// the age distinguishes fresh from stale.
//
// The reset offsets sit in the INTERIOR of formatDuration's buckets, not on
// their edges. The obvious 3_600 renders '1h' only while the clock agrees to
// the second: this suite runs on real time against a real NowProvider tick, and
// one second of drift puts the span at 3_599, below the hour branch, rendering
// '60m' and failing a countdown assertion for no reason the output explains.
const FIVE_HOUR_RESET_IN = 7_200;
const SEVEN_DAY_RESET_IN = 172_800;

function observed(ageSeconds: number, fiveHourPercent = 62, sevenDayPercent = 18) {
  return {
    state: 'observed' as const,
    observed_at: NOW_SECONDS - ageSeconds,
    session_id: 'sess-1',
    five_hour: { used_percentage: fiveHourPercent, resets_at: NOW_SECONDS + FIVE_HOUR_RESET_IN },
    seven_day: { used_percentage: sevenDayPercent, resets_at: NOW_SECONDS + SEVEN_DAY_RESET_IN },
    reason: '',
  };
}

function renderAccounts(tickMs?: number) {
  // No router wrapper: the page renders no Link and reads no route params, so
  // adding one would only import react-router's v7 deprecation warning, which
  // this suite's setup turns into a failure.
  return render(
    <NowProvider {...(tickMs === undefined ? {} : { intervalMs: tickMs })}>
      <AccountsPage />
    </NowProvider>,
  );
}

function groupSection(label: string): HTMLElement {
  const heading = screen.getByRole('heading', { name: new RegExp(label, 'i') });
  const section = heading.closest('section');
  if (section === null) throw new Error(`no section for group ${label}`);
  return section;
}

beforeEach(() => {
  NOW_SECONDS = Math.floor(Date.now() / 1_000);
  invalidate('accounts:quota');
  currentReport = emptyReport();
  fetchMode = 'ok';
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (!url.includes('/api/account-quota')) throw new Error(`unexpected fetch ${url}`);
      if (fetchMode === 'fail') {
        return new Response(JSON.stringify({ error: 'plane offline' }), {
          status: 503,
          headers: { 'content-type': 'application/json' },
        });
      }
      return new Response(JSON.stringify(currentReport), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      });
    }),
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it('renders a fresh reading as a percentage with its age', async () => {
  currentReport = {
    ...emptyReport(),
    accounts: [entry('2', { in_pool: true, observation: observed(180) })],
    pool: ['2'],
  };
  renderAccounts();

  const row = await screen.findByTestId('account-row-2');
  expect(within(row).getByTestId('five-hour-2').textContent ?? '').toContain('62%');
  // The age is the whole point of showing the number at all.
  expect(within(row).getByTestId('five-hour-2').textContent ?? '').toMatch(/as of 3m ago/i);
  expect(within(row).getByTestId('seven-day-2').textContent ?? '').toContain('18%');
});

it('shows a stale reading, with its age, visibly grayed rather than hidden', async () => {
  // Both rows in ONE render, because "grayed" is a claim about a DIFFERENCE.
  // Asserting only dataset.reading === 'stale' -- an attribute that exists for
  // this test and renders nothing -- let a mutation that made the class
  // unconditional and dropped the '(stale)' marker pass the whole suite, with
  // a stale reading pixel-identical to a fresh one.
  currentReport = {
    ...emptyReport(),
    accounts: [
      entry('2', { in_pool: true, observation: observed(WELL_OUTSIDE) }),
      entry('3', { in_pool: true, observation: observed(60) }),
    ],
    pool: ['2', '3'],
  };
  renderAccounts();

  const cell = await screen.findByTestId('five-hour-2');
  const fresh = await screen.findByTestId('five-hour-3');
  // Shown, not hidden: an operator who cannot see the last known level cannot
  // tell a quiet account from a capped one.
  expect(cell.textContent ?? '').toContain('62%');
  expect(cell.textContent ?? '').toMatch(/as of 25m ago/i);
  expect(cell.dataset.reading).toBe('stale');
  // Visible in text, for anyone reading the row rather than its colors.
  expect(cell.textContent ?? '').toMatch(/\(stale\)/);
  expect(fresh.textContent ?? '').not.toMatch(/\(stale\)/);
  // Visible in color. Compared against the fresh cell rather than matched
  // against a literal class name, so restyling the palette does not fail this
  // and dropping the distinction cannot pass it.
  expect(cell.className).not.toBe(fresh.className);
});

it('advances the displayed age between refetches, not only on one', async () => {
  // The staleness rule is worth nothing if the age it is applied to is frozen.
  // Capturing now once at mount -- the obvious `useRef(Date.now())` -- left the
  // whole suite green while a tab open for an hour reported "as of 3m ago" in
  // full-brightness text forever, because every other test asserts a single
  // settled render.
  //
  // Real timers, no fakes: NowProvider reads the wall clock, so only real
  // elapsed time can move the age. The tick is driven fast and the fixture is
  // placed 10s back, which puts the next second boundary under a second away --
  // the poll interval is 30s, so nothing refetches inside this window and the
  // change can ONLY have come from the tick.
  currentReport = {
    ...emptyReport(),
    accounts: [entry('2', { in_pool: true, observation: observed(10) })],
    pool: ['2'],
  };
  renderAccounts(20);

  const cell = await screen.findByTestId('five-hour-2');
  expect(cell.textContent ?? '').toMatch(/as of 10s ago/);
  await waitFor(() => expect(cell.textContent ?? '').toMatch(/as of 1[12]s ago/), {
    timeout: 4_000,
  });
});

it('says how long until each window resets, counting forward from now', async () => {
  // formatRelative is the helper every other timestamp on this row goes
  // through, and it clamps future timestamps to 'now' -- a countdown routed
  // through it renders "resets in now" on every live window, which reads as a
  // window about to reset rather than one with hours left. Only a forward
  // formatter produces these two.
  currentReport = {
    ...emptyReport(),
    accounts: [entry('2', { in_pool: true, observation: observed(180) })],
    pool: ['2'],
  };
  renderAccounts();

  const row = await screen.findByTestId('account-row-2');
  expect(within(row).getByTestId('five-hour-2').textContent ?? '').toMatch(/resets in 2h/i);
  // Asserted per window rather than on the row, so a countdown wired to one
  // window's boundary and rendered under both cells cannot pass.
  expect(within(row).getByTestId('seven-day-2').textContent ?? '').toMatch(/resets in 2d/i);
});

it('leaves the countdown ungrayed on a stale row, where the percentage is grayed', async () => {
  // The distinction the whole cell exists to make: the percentage decayed with
  // the window and the boundary did not move, so graying both would report an
  // exact fact as doubtful. Both accounts render together because "ungrayed"
  // is a claim about a difference -- against a single row, making the
  // countdown's class unconditional passes whatever the cell is doing.
  currentReport = {
    ...emptyReport(),
    accounts: [
      entry('2', { in_pool: true, observation: observed(WELL_OUTSIDE) }),
      entry('3', { in_pool: true, observation: observed(60) }),
    ],
    pool: ['2', '3'],
  };
  renderAccounts();

  const staleCell = await screen.findByTestId('five-hour-2');
  const staleReset = await screen.findByTestId('five-hour-2-reset');
  const freshReset = await screen.findByTestId('five-hour-3-reset');
  expect(staleCell.dataset.reading).toBe('stale');
  expect(staleReset.textContent ?? '').toMatch(/resets in 2h/i);
  // The countdown is also its own clause in the text, so the row still reads
  // correctly with no color at all: '(stale)' attaches to the percentage and
  // nothing about the countdown claims to be stale.
  expect(staleCell.textContent ?? '').toMatch(/\(stale\)/);
  expect(staleReset.textContent ?? '').not.toMatch(/\(stale\)/);
  // Class-level claims, and only that: jsdom loads no stylesheet, so nothing
  // here can see a rendered color. Together they still bound the failure --
  // the countdown overrides its grayed parent rather than inheriting it, and
  // it looks the same whether the row is stale or fresh.
  expect(staleReset.className).not.toBe(staleCell.className);
  expect(staleReset.className).toBe(freshReset.className);
  // Non-empty on purpose: with the class dropped entirely, both comparisons
  // above still hold while the span quietly inherits the cell's gray, which is
  // exactly the state this test exists to forbid.
  expect(staleReset.className.trim()).not.toBe('');
});

it('withholds the percentage once the window has rolled', async () => {
  const rolled = {
    ...observed(WELL_OUTSIDE),
    five_hour: { used_percentage: 62, resets_at: NOW_SECONDS - 60 },
  };
  currentReport = {
    ...emptyReport(),
    accounts: [entry('2', { in_pool: true, observation: rolled })],
    pool: ['2'],
  };
  renderAccounts();

  const cell = await screen.findByTestId('five-hour-2');
  expect(cell.textContent ?? '').not.toContain('62%');
  expect(cell.textContent ?? '').toMatch(/unknown/i);
  expect(cell.dataset.reading).toBe('rolled');
  // No countdown either, and this is the case a reader will look for: the
  // boundary on record is the one that already passed, so any countdown here
  // is invented. resets_at is 60s BEHIND now, so an implementation that
  // formats it anyway renders "resets in 0s" -- an imminent reset, on a window
  // that ended a minute ago.
  expect(cell.textContent ?? '').not.toMatch(/resets in/i);
  expect(screen.queryByTestId('five-hour-2-reset')).toBeNull();
  // The 7d window on the same record is untouched: the two roll independently
  // and a rolled 5h says nothing about the 7d level.
  const sevenDay = await screen.findByTestId('seven-day-2');
  expect(sevenDay.textContent ?? '').toContain('18%');
  expect(sevenDay.textContent ?? '').toMatch(/resets in 2d/i);
});

it('tells never-observed, no-limits, and unreadable apart in the rendered row', async () => {
  currentReport = {
    ...emptyReport(),
    accounts: [
      entry('2', { in_pool: true }),
      entry('3', {
        in_pool: true,
        observation: {
          state: 'no_limits',
          observed_at: NOW_SECONDS - JUST_INSIDE,
          session_id: 's',
          five_hour: null,
          seven_day: null,
          reason: '',
        },
      }),
      entry('4', {
        in_pool: true,
        observation: {
          state: 'unreadable',
          observed_at: null,
          session_id: '',
          five_hour: null,
          seven_day: null,
          reason: 'parsing 4.json: unexpected end of JSON input',
        },
      }),
    ],
    pool: ['2', '3', '4'],
  };
  renderAccounts();

  const never = await screen.findByTestId('account-row-2');
  expect(never.textContent ?? '').toMatch(/never observed/i);
  // Never-observed must not render as a zero: a 0% reads as an idle healthy
  // account, when in fact nothing has ever looked.
  expect(never.textContent ?? '').not.toContain('0%');

  const noLimits = await screen.findByTestId('account-row-3');
  expect(noLimits.textContent ?? '').toMatch(/no limits reported/i);
  expect(noLimits.textContent ?? '').not.toMatch(/never observed/i);
  // WHEN it reported is what separates "the payload has been limitless for
  // hours" from "it just reported without limits". Without this the row's
  // observed_at branch collapses to the null one with the suite still green,
  // and JUST_INSIDE pins nothing.
  expect(noLimits.textContent ?? '').toMatch(/14m ago/);

  const unreadable = await screen.findByTestId('account-row-4');
  expect(unreadable.textContent ?? '').toMatch(/unreadable/i);
  // The reason is the only thing that lets an operator find the bad writer.
  expect(unreadable.textContent ?? '').toMatch(/unexpected end of JSON input/i);
});

it('renders a percentage without the collector arithmetic noise', async () => {
  // The value account 5 actually reported on this host. The wire carries it
  // verbatim -- confirmed by re-encoding the real file through the Go structs
  // -- so a row would read "14.000000000000002%" if the render did not format
  // it. No fixture with a clean 62.0 could catch this.
  currentReport = {
    ...emptyReport(),
    accounts: [entry('2', { in_pool: true, observation: observed(60, 100, 14.000000000000002) })],
    pool: ['2'],
  };
  renderAccounts();

  const sevenDay = await screen.findByTestId('seven-day-2');
  expect(sevenDay.textContent ?? '').toContain('14%');
  expect(sevenDay.textContent ?? '').not.toContain('14.000000000000002');
  // A whole 100 still renders whole, not as "100.0".
  expect((await screen.findByTestId('five-hour-2')).textContent ?? '').toContain('100%');
});

it('reports the rotation context that explains why a level is as old as it is', async () => {
  // The bottom line of every row, and previously asserted by nothing: an
  // early-return of '' passed the entire suite. Cooldown matters most -- an
  // account parked at a modest percentage looks like spare capacity, and the
  // percentage alone cannot say otherwise.
  currentReport = {
    ...emptyReport(),
    accounts: [
      entry('2', {
        in_pool: true,
        observation: observed(WELL_OUTSIDE),
        last_used_at: NOW_SECONDS - 7_200,
        cooldown_until: NOW_SECONDS + 1_800,
        bound_sessions: 3,
        suspect_sessions: 2,
      }),
    ],
    pool: ['2'],
  };
  renderAccounts();

  const row = await screen.findByTestId('account-row-2');
  const text = row.textContent ?? '';
  expect(text).toMatch(/last used 2h ago/i);
  expect(text).toMatch(/3 bindings on record/i);
  expect(text).toMatch(/2 showing the cap marker/i);
  // Forward duration, not a past one. The mirror-arithmetic this replaced
  // rendered the right number today and would have inverted silently on any
  // change to formatRelative's clamping.
  expect(text).toMatch(/cooling down for 30m/i);
});

it('groups the pool apart from the accounts held out of it', async () => {
  currentReport = {
    ...emptyReport(),
    accounts: [
      entry('0', { label: 'operator interactive' }),
      entry('1', { label: 'orchestrator pin' }),
      entry('2', { in_pool: true, observation: observed(60) }),
      entry('3', { in_pool: true, observation: observed(60) }),
    ],
    pool: ['2', '3'],
  };
  renderAccounts();
  await screen.findByTestId('account-row-2');

  const pool = groupSection('Rotation pool');
  expect(within(pool).getByTestId('account-row-2')).not.toBeNull();
  expect(within(pool).getByTestId('account-row-3')).not.toBeNull();
  // The whole reason the grouping exists: an operator must not read four rows
  // as four rotating accounts.
  expect(within(pool).queryByTestId('account-row-0')).toBeNull();
  expect(within(pool).queryByTestId('account-row-1')).toBeNull();
  expect(within(groupSection('operator interactive')).getByTestId('account-row-0')).not.toBeNull();
  expect(within(groupSection('orchestrator pin')).getByTestId('account-row-1')).not.toBeNull();
});

it('renders a label on a pooled account, which no group heading would show', async () => {
  // Grouping keys pooled accounts by membership, not by label, so without this
  // the operator's configured name for an in-pool account renders nowhere and
  // the config silently does nothing.
  currentReport = {
    ...emptyReport(),
    accounts: [entry('2', { in_pool: true, label: 'burst capacity', observation: observed(60) })],
    pool: ['2'],
  };
  renderAccounts();

  const row = await screen.findByTestId('account-row-2');
  expect(row.textContent ?? '').toContain('burst capacity');
});

it('says where it looked when nothing has ever been collected', async () => {
  currentReport = {
    ...emptyReport(),
    rotation: { available: false, reason: '/home/op/.claude-homes/rotation.json does not exist' },
  };
  renderAccounts();

  // An empty tab that does not say where it read from sends the operator
  // hunting through the dashboard rather than the collector.
  const empty = await screen.findByTestId('accounts-empty');
  expect(empty.textContent ?? '').toContain('/home/op/.claude-homes');
  expect(empty.textContent ?? '').toMatch(/rotation\.json does not exist/i);
});

it('surfaces a rotation read failure without hiding the readings it still has', async () => {
  currentReport = {
    ...emptyReport(),
    accounts: [entry('2', { observation: observed(60) })],
    rotation: { available: false, reason: 'parsing rotation.json: unexpected end of JSON input' },
  };
  renderAccounts();

  expect((await screen.findByTestId('five-hour-2')).textContent ?? '').toContain('62%');
  expect(screen.getByRole('alert').textContent ?? '').toMatch(/unexpected end of JSON input/i);
});

it('reports suspects no binding could attribute rather than dropping them', async () => {
  currentReport = { ...emptyReport(), unattributed_suspects: 2 };
  renderAccounts();
  expect(await screen.findByText(/2 suspect sessions/i)).not.toBeNull();
});

it('degrades to a message, not a blank page, when the endpoint is down', async () => {
  fetchMode = 'fail';
  renderAccounts();
  expect((await screen.findByRole('alert')).textContent ?? '').toMatch(/plane offline/i);
});
