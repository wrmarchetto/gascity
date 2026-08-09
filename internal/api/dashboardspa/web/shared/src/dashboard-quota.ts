// Account quota — the wire shape of GET /api/account-quota (mirrors the Go
// structs in internal/api/dashboardbff/quota.go) plus the rules that turn a raw
// reading into something safe to render.
//
// Why the interpretation lives here rather than in the Go handler: the two
// fields age completely differently, and one of them ages against the clock of
// whoever is LOOKING at the page.
//
//   used_percentage  decays. The 5h window keeps rolling with no session on
//                    the account, so a 40-minute-old 62% is not today's 62%.
//   resets_at        is an absolute epoch. It stays correct at any age, right
//                    up until it passes -- after which the window has rolled
//                    and the percentage is meaningless, not merely stale.
//
// A server-side classification would freeze "as of 2m ago" onto a tab left open
// for an hour, which is the confident-wrong-number failure this whole view
// exists to prevent. So the server passes both fields through verbatim and the
// classification below runs per render against the browser's clock (driven by
// NowProvider's tick).
//
// Verified by dashboard-quota.test.ts.

/**
 * How old a reading may be before it is shown grayed. The operator's call:
 * show the value with its age and gray it past this bound -- never hide it and
 * never silently refresh it, because both hide the fact that nothing is
 * currently observing that account.
 */
export const QUOTA_STALE_AFTER_SECONDS = 15 * 60;

/** Label for the group holding the accounts rotation actually schedules. */
export const ROTATION_POOL_GROUP_LABEL = 'Rotation pool';

/**
 * Label for out-of-pool accounts with no configured name. Deliberately states
 * only what the data supports: the dashboard knows an account is outside the
 * pool, not why it is being held out.
 */
export const UNLABELED_GROUP_LABEL = 'Not in rotation';

/**
 * Which of the mutually exclusive presence states an account's record is in.
 * These never collapse: `never_observed` says nothing has run on the account
 * since collection was wired, `no_limits` says something did and the payload
 * carried no limits, and `unreadable` says a record exists and cannot be
 * trusted. Rendering any of them as a zeroed reading would show an idle-looking
 * healthy account, which is the dangerous misread.
 */
export type AccountObservationState = 'never_observed' | 'no_limits' | 'observed' | 'unreadable';

export interface AccountQuotaWindow {
  readonly used_percentage: number;
  readonly resets_at: number;
}

export interface AccountObservation {
  readonly state: AccountObservationState;
  /** Epoch seconds the reading was taken; null unless a record was readable. */
  readonly observed_at: number | null;
  readonly session_id: string;
  readonly five_hour: AccountQuotaWindow | null;
  readonly seven_day: AccountQuotaWindow | null;
  /** Why the record is unreadable; empty in every other state. */
  readonly reason: string;
}

export interface AccountQuotaEntry {
  readonly account: string;
  /** Operator-configured group name, empty when unconfigured. */
  readonly label: string;
  readonly in_pool: boolean;
  readonly observation: AccountObservation;
  readonly last_used_at: number | null;
  readonly cooldown_until: number | null;
  /** Rotation bindings on record -- not proof any session is still live. */
  readonly bound_sessions: number;
  readonly suspect_sessions: number;
}

export interface AccountRotationState {
  readonly available: boolean;
  readonly reason: string;
}

export interface AccountQuotaReport {
  readonly accounts: readonly AccountQuotaEntry[];
  readonly pool: readonly string[];
  readonly rotation: AccountRotationState;
  readonly homes_dir: string;
  readonly unattributed_suspects: number;
}

/**
 * `current` — young enough to read as today's number.
 * `stale` — shown, with its age, grayed.
 * `rolled` — the window ended, so the percentage is withheld entirely.
 */
export type QuotaReadingState = 'current' | 'stale' | 'rolled';

export interface QuotaReading {
  readonly state: QuotaReadingState;
  /** Null in the `rolled` state: the last-seen number describes a dead window. */
  readonly percentage: number | null;
  /** Seconds since the reading was taken, clamped at zero for clock skew. */
  readonly ageSeconds: number;
  readonly resetsAt: number;
  /**
   * Seconds from now until the window ends, null in the `rolled` state.
   *
   * Measured from NOW rather than from the observation, which is what makes it
   * the one number on a row that an old reading does not degrade: the boundary
   * is fixed and only the clock moves toward it. Every other field here is
   * age-relative, so the natural-looking `resetsAt - observedAt` is wrong by
   * exactly the reading's age.
   *
   * Null in `rolled` and only there. The boundary that passed is not the next
   * one and nothing observed the next one, so there is no countdown to give --
   * including the future-dated case where `resetsAt - nowSeconds` is still
   * positive and would hand back a number for a window already over.
   */
  readonly secondsUntilReset: number | null;
}

/**
 * Classify one window's reading as of `nowSeconds`.
 *
 * The rolled check runs FIRST and is not conditioned on age, because freshness
 * cannot rescue a dead window: a reading taken one second ago whose window ends
 * this second still describes a window that no longer exists. Ordering it the
 * other way would let any recent reading through and would render 62% for an
 * account whose 5h window reset minutes ago.
 */
export function classifyQuotaReading(
  observedAt: number,
  window: AccountQuotaWindow,
  nowSeconds: number,
): QuotaReading {
  // Clamped: a future observed_at means the collector host and the browser
  // disagree about the time, and "as of -3m ago" reads as a bug in the tab.
  const ageSeconds = Math.max(0, Math.round(nowSeconds - observedAt));
  if (window.resets_at <= nowSeconds || window.resets_at < observedAt) {
    return {
      state: 'rolled',
      percentage: null,
      ageSeconds,
      resetsAt: window.resets_at,
      secondsUntilReset: null,
    };
  }
  return {
    state: ageSeconds > QUOTA_STALE_AFTER_SECONDS ? 'stale' : 'current',
    percentage: window.used_percentage,
    ageSeconds,
    resetsAt: window.resets_at,
    // Strictly positive here: reaching this branch means resets_at > nowSeconds.
    secondsUntilReset: window.resets_at - nowSeconds,
  };
}

/**
 * Render a used-percentage for display, at one decimal place.
 *
 * The collector's arithmetic emits float noise -- account 5 on this host
 * reported 14.000000000000002 for its 7d window -- and interpolating the raw
 * number renders that verbatim, which reads as a broken tab rather than as a
 * capacity level.
 *
 * Rounding happens HERE, at the render edge, and never on the wire or in
 * `classifyQuotaReading`: the raw value has to survive intact so the browser
 * can do its own interpretation, which is the entire argument of this module.
 *
 * One decimal rather than none, because the collector does emit real halves
 * (62.5) and rounding those to whole percent would misreport a level the
 * operator may be watching against a cap.
 */
export function formatQuotaPercentage(value: number): string {
  return String(Math.round(value * 10) / 10);
}

export interface AccountGroup {
  readonly label: string;
  /** True only for the rotation-pool group, which the tab renders first. */
  readonly inPool: boolean;
  readonly accounts: readonly AccountQuotaEntry[];
}

/**
 * Split the report into the groups the tab renders, pool first.
 *
 * The pool group is always emitted, even empty: an emptied pool and a dashboard
 * with no pool concept would otherwise look identical, and the first is a
 * capacity incident. Out-of-pool accounts group by their configured label so
 * the reasons an account is held out stay in configuration -- this file names
 * no role, and neither does the Go side.
 *
 * Group order is pool, then labeled groups in first-appearance order (the
 * report arrives sorted by account id, so this is ascending id), then the
 * unlabeled remainder. Stable across polls so rows do not shuffle under a
 * reader mid-diagnosis.
 */
export function groupAccountsForDisplay(report: AccountQuotaReport): readonly AccountGroup[] {
  const pool: AccountQuotaEntry[] = [];
  const held = new Map<string, AccountQuotaEntry[]>();

  for (const account of report.accounts) {
    if (account.in_pool) {
      pool.push(account);
      continue;
    }
    const label = account.label === '' ? UNLABELED_GROUP_LABEL : account.label;
    const existing = held.get(label);
    if (existing === undefined) held.set(label, [account]);
    else existing.push(account);
  }

  const groups: AccountGroup[] = [
    { label: ROTATION_POOL_GROUP_LABEL, inPool: true, accounts: pool },
  ];
  for (const [label, accounts] of held) {
    groups.push({ label, inPool: false, accounts });
  }
  return groups;
}
