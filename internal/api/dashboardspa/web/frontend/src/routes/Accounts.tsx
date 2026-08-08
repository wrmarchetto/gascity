// The Accounts view: per-account 5h and 7d rate-limit levels, grouped by
// whether the rotation actually schedules the account.
//
// Every number here was written by a statusline that only fires while a session
// is live on that account, so an idle account's levels freeze at whatever it
// last reported. The view is built around that single fact: no value is ever
// shown without its age, anything past the threshold is grayed, and a value
// whose window has since rolled is withheld entirely rather than shown as the
// last-seen number. The rules live in shared/src/dashboard-quota.ts and are
// applied per render against NowProvider's tick -- see that file for why the
// server cannot do this classification.
//
// Absences worth knowing about, because a reader will look for them:
//
//   - No live stream. New readings appear only when some other session renders
//     a statusline, which no event on our bus corresponds to, so the tab polls
//     while visible at the same 30s cadence as Health and Activity. The
//     displayed ages advance every second regardless, off NowProvider's tick --
//     an age that only moved on refetch would understate how stale a value is.
//   - No per-session binding list. rotation.json's bindings map is not pruned
//     when a session ends, so the counts here are bindings ON RECORD. Rendering
//     the session names would read as a live roster and would be wrong.
//   - No "which account is which role". The dashboard is told the group names
//     by configuration (DASHBOARD_ACCOUNT_LABELS) and invents none.

import type { ReactNode } from 'react';
import { useCallback } from 'react';
import {
  QUOTA_STALE_AFTER_SECONDS,
  classifyQuotaReading,
  formatQuotaPercentage,
  groupAccountsForDisplay,
  UNLABELED_GROUP_LABEL,
  type AccountGroup,
  type AccountQuotaEntry,
  type AccountQuotaWindow,
} from 'gas-city-dashboard-shared';
import { api } from '../api/client';
import { Button } from '../components/Button';
import { PageHeader } from '../components/PageHeader';
import { StatusBadge, type StatusTone } from '../components/StatusBadge';
import { useNow } from '../contexts/NowContext';
import { formatDuration, formatRelative } from '../hooks/time';
import { useCachedData } from '../hooks/useCachedData';
import { useVisibleRefresh } from '../hooks/useVisibleRefresh';

// Matches Health and Activity. The underlying files change only when another
// session renders, so a tighter poll would mostly re-read the same bytes.
const ACCOUNT_QUOTA_POLL_MS = 30_000;

export function AccountsPage() {
  const nowMs = useNow();
  const nowSeconds = Math.floor(nowMs / 1_000);
  const quota = useCachedData('accounts:quota', () => api.accountQuota());
  const refresh = quota.refresh;
  const reload = useCallback(async () => {
    await refresh();
  }, [refresh]);
  useVisibleRefresh(reload, ACCOUNT_QUOTA_POLL_MS);

  const report = quota.data ?? null;
  const groups = report === null ? [] : groupAccountsForDisplay(report);

  return (
    <section aria-labelledby="accounts-title">
      <PageHeader
        title="Accounts"
        synopsis={
          report === null
            ? 'Reading per-account rate-limit levels.'
            : `5h and 7d levels per account, as last observed. Values older than ` +
              `${QUOTA_STALE_AFTER_SECONDS / 60} minutes are grayed; a window that has ` +
              `since rolled shows unknown rather than its last level.`
        }
        meta={
          <Button size="sm" onClick={() => void reload()}>
            {quota.loading && report === null ? 'Loading' : 'Refresh'}
          </Button>
        }
      />

      {quota.error !== null && (
        <p role="alert" className="mb-8 text-body text-accent">
          Account levels unavailable: {quota.error}
        </p>
      )}

      {report !== null && !report.rotation.available && (
        <p role="alert" className="mb-8 text-body text-warn">
          Pool membership unknown: {report.rotation.reason}. Every account below is shown ungrouped
          until rotation state can be read.
        </p>
      )}

      {report !== null && report.unattributed_suspects > 0 && (
        <p className="mb-8 text-body text-warn">
          {report.unattributed_suspects} suspect sessions have no rotation binding, so they belong
          to no account below.
        </p>
      )}

      {report !== null && report.accounts.length === 0 ? (
        <p data-testid="accounts-empty" className="text-body text-fg-muted">
          No accounts found under <code className="text-fg">{report.homes_dir}</code>.
          {report.rotation.available ? '' : ` ${report.rotation.reason}.`}
        </p>
      ) : (
        <div className="space-y-12">
          {groups.map((group) => (
            <AccountGroupSection key={group.label} group={group} nowSeconds={nowSeconds} />
          ))}
        </div>
      )}
    </section>
  );
}

function AccountGroupSection({ group, nowSeconds }: { group: AccountGroup; nowSeconds: number }) {
  const headingId = `accounts-group-${group.label.replace(/\s+/g, '-').toLowerCase()}`;
  return (
    <section aria-labelledby={headingId} className="space-y-4">
      <div className="flex items-baseline gap-3 border-b border-rule pb-2">
        <h2 id={headingId} className="text-title font-semibold tracking-tight text-fg">
          {group.label}
        </h2>
        <span className="text-label uppercase tracking-wider text-fg-muted">
          {group.accounts.length} {group.accounts.length === 1 ? 'account' : 'accounts'}
          {group.inPool ? ' rotating' : ' held out of rotation'}
        </span>
      </div>
      {group.accounts.length === 0 ? (
        <p className="text-body text-warn">
          Nothing is in the rotation pool. Every session will land on whatever the launcher falls
          back to.
        </p>
      ) : (
        <ul className="space-y-4">
          {group.accounts.map((account) => (
            <AccountRow key={account.account} account={account} nowSeconds={nowSeconds} />
          ))}
        </ul>
      )}
      {group.label === UNLABELED_GROUP_LABEL && (
        // The only place an operator will wonder why these accounts are held
        // out, so it is the only place worth naming the knob. The dashboard
        // cannot answer the question itself -- see the file header.
        <p className="text-label uppercase tracking-wider text-fg-muted">
          Name these groups with DASHBOARD_ACCOUNT_LABELS, e.g. &quot;0=operator
          interactive,1=orchestrator pin&quot;
        </p>
      )}
    </section>
  );
}

function AccountRow({ account, nowSeconds }: { account: AccountQuotaEntry; nowSeconds: number }) {
  const { observation } = account;
  return (
    <li
      data-testid={`account-row-${account.account}`}
      className="grid grid-cols-1 gap-x-6 gap-y-2 border border-rule rounded-sm px-4 py-3 md:grid-cols-[8rem_minmax(0,1fr)_minmax(0,1fr)]"
    >
      <div className="space-y-1">
        <p className="text-body font-semibold text-fg">account{account.account}</p>
        {/* A label on a POOLED account has no group heading to appear in
            (grouping puts pool membership first), so render it here or the
            operator's configured name would be silently dropped. Out-of-pool
            labels are already the heading above and are not repeated. */}
        {account.in_pool && account.label !== '' && (
          <p className="text-label uppercase tracking-wider text-fg-muted">{account.label}</p>
        )}
        <ObservationBadge state={observation.state} />
      </div>

      {observation.state === 'observed' ? (
        <>
          <WindowCell
            testId={`five-hour-${account.account}`}
            label="5h"
            window={observation.five_hour}
            observedAt={observation.observed_at}
            nowSeconds={nowSeconds}
          />
          <WindowCell
            testId={`seven-day-${account.account}`}
            label="7d"
            window={observation.seven_day}
            observedAt={observation.observed_at}
            nowSeconds={nowSeconds}
          />
        </>
      ) : (
        <p className="text-body text-fg-muted md:col-span-2">
          {observationExplanation(account, nowSeconds)}
        </p>
      )}

      <p className="text-label uppercase tracking-wider text-fg-muted md:col-span-3">
        {rotationContext(account, nowSeconds)}
      </p>
    </li>
  );
}

/**
 * One window's cell. The reading state rides on `data-reading` as well as the
 * color, so the distinction survives the greyscale test and is assertable
 * without matching on class names.
 */
function WindowCell({
  testId,
  label,
  window,
  observedAt,
  nowSeconds,
}: {
  testId: string;
  label: string;
  window: AccountQuotaWindow | null;
  observedAt: number | null;
  nowSeconds: number;
}) {
  if (window === null || observedAt === null) {
    return (
      <p data-testid={testId} data-reading="absent" className="text-body text-fg-muted">
        {label}: not reported
      </p>
    );
  }
  const reading = classifyQuotaReading(observedAt, window, nowSeconds);
  const age = formatRelative(observedAt * 1_000, nowSeconds * 1_000);
  return (
    <p
      data-testid={testId}
      data-reading={reading.state}
      className={`text-body ${reading.state === 'current' ? 'text-fg' : 'text-fg-muted'}`}
    >
      <span className="text-label uppercase tracking-wider text-fg-muted">{label}</span>{' '}
      {reading.percentage === null ? (
        <>
          unknown — window rolled {formatRelative(reading.resetsAt * 1_000, nowSeconds * 1_000)}{' '}
          ago, after the last reading
        </>
      ) : (
        <>
          {formatQuotaPercentage(reading.percentage)}%, as of {age} ago
          {reading.state === 'stale' ? ' (stale)' : ''}
        </>
      )}
    </p>
  );
}

const OBSERVATION_TONE: Record<AccountQuotaEntry['observation']['state'], StatusTone> = {
  observed: 'ok',
  no_limits: 'neutral',
  never_observed: 'neutral',
  unreadable: 'warn',
};

const OBSERVATION_LABEL: Record<AccountQuotaEntry['observation']['state'], string> = {
  observed: 'observed',
  no_limits: 'no limits reported',
  never_observed: 'never observed',
  unreadable: 'unreadable',
};

function ObservationBadge({ state }: { state: AccountQuotaEntry['observation']['state'] }) {
  return <StatusBadge tone={OBSERVATION_TONE[state]} label={OBSERVATION_LABEL[state]} />;
}

/**
 * Prose for the three non-observed states. Each says what is and is not known,
 * because the states are easy to conflate and the wrong reading sends an
 * operator to the wrong subsystem: "never observed" is a collection question,
 * "no limits reported" is an upstream-payload question, and "unreadable" is a
 * broken-writer question.
 */
function observationExplanation(account: AccountQuotaEntry, nowSeconds: number): string {
  const { observation } = account;
  switch (observation.state) {
    case 'never_observed':
      return 'No session has reported levels for this account since collection was wired.';
    case 'no_limits':
      return observation.observed_at === null
        ? 'A session reported, but its payload carried no rate limits.'
        : `A session reported ${formatRelative(observation.observed_at * 1_000, nowSeconds * 1_000)} ago, but its payload carried no rate limits.`;
    case 'unreadable':
      return `The record for this account could not be read: ${observation.reason}`;
    case 'observed':
      // Unreachable: the caller renders the window cells for this state and
      // never asks for prose. Listed rather than folded into a `default` so a
      // new state added to the union fails the exhaustiveness check here
      // instead of silently rendering an empty row.
      return '';
  }
}

/**
 * The rotation-side facts, which explain WHY an account's numbers are as old as
 * they are. Cooldown is named explicitly rather than folded into the level: an
 * account can be parked with a modest percentage and the percentage alone would
 * make that look like spare capacity.
 */
function rotationContext(account: AccountQuotaEntry, nowSeconds: number): ReactNode {
  const parts: string[] = [];
  parts.push(
    account.last_used_at === null
      ? 'never handed to a session'
      : `last used ${formatRelative(account.last_used_at * 1_000, nowSeconds * 1_000)} ago`,
  );
  parts.push(`${account.bound_sessions} bindings on record`);
  if (account.suspect_sessions > 0) {
    parts.push(`${account.suspect_sessions} showing the cap marker`);
  }
  if (account.cooldown_until !== null && account.cooldown_until > nowSeconds) {
    parts.push(`cooling down for ${formatDuration(account.cooldown_until - nowSeconds)}`);
  }
  return parts.join(' · ');
}
