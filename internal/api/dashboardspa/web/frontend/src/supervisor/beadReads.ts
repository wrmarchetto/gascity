import type {
  Bead,
  GetV0CityByCityNameBeadsData,
  ListBodyBead,
} from 'gas-city-dashboard-shared/gc-supervisor';
import { activeCityOrThrow } from '../api/cityBase';
import { SupervisorApiError, supervisorApi } from './client';

export type SupervisorBead = Bead;

export interface SupervisorBeadList extends Omit<ListBodyBead, 'items' | 'total'> {
  items: SupervisorBead[];
  total: number;
  upstream_total?: number;
  upstream_fetched: number;
  fetch_limit: number;
}

export interface ListSupervisorBeadsOptions {
  includeClosed?: boolean;
  /**
   * Reveal the rows gc itself does not count as work -- external-message
   * transcripts, session and order bookkeeping, and the bookkeeping bead
   * types. Off by default; the Beads board exposes it as a control so those
   * rows stay reachable rather than being removed (ci-zg9lbn). The families
   * are not enumerated here on purpose: the supervisor serves them.
   */
  includeBookkeeping?: boolean;
  rigFilter?: string;
  limit?: number;
  city?: string;
  signal?: AbortSignal;
}

// Pre-exposure load bounds (gascity-dashboard-q89b): board and detail-fallback
// list fetches run per client, so keep them modest. Truncation degrades
// visibly: upstream_total vs fetch_limit feeds the board's truncation notice.
const BEADS_FETCH_LIMIT = 1000;
const ASSIGNED_BEADS_FETCH_LIMIT = 200;
const DETAIL_FALLBACK_FETCH_LIMIT = 1000;
// The "real work" bead types the board fans out (one typed query each),
// then keeps via isWorkBead — the bookkeeping types
// (message/session/molecule/…) are dropped. Every entry MUST be a type the
// live gc/bd backend accepts as a `type=` filter: a rig-scoped include-closed
// (`all=true`) query for a type the backend rejects fails closed (HTTP 503
// "invalid issue type") and that one rejected leg blanks the whole board.
//
// Kept as an allowlist rather than switched to the supervisor's ready-excluded
// TYPE set, which is a denylist: the allowlist also drops a type gc gains
// later and nobody has decided belongs on a work board. The LABEL dimension
// went the other way for exactly the reason the type one did not -- families
// are added there routinely, so that list must be read, not guessed.
const ENGINEERING_BEAD_TYPES: ReadonlySet<string> = new Set([
  'feature',
  'bug',
  'task',
  'epic',
  'chore',
  'decision',
]);

export async function listSupervisorBeads(
  options: ListSupervisorBeadsOptions = {},
): Promise<SupervisorBeadList> {
  const cityName = options.city ?? activeCityOrThrow('list supervisor beads');
  const limit = options.limit ?? BEADS_FETCH_LIMIT;
  const rigFilter = options.rigFilter?.trim() ?? '';
  const includeClosed = options.includeClosed ?? false;
  const includeBookkeeping = options.includeBookkeeping ?? false;
  const baseQuery: NonNullable<GetV0CityByCityNameBeadsData['query']> = {
    limit,
    ...(includeClosed ? { all: true } : {}),
    ...(rigFilter.length === 0 ? {} : { rig: rigFilter }),
  };
  // The hidden-label set is READ from the supervisor, never typed here: gc
  // owns it (internal/beads.readyExcludeLabels) and adds external-message
  // families to it over time, so a copy on this side rots silently -- the
  // whole point of ci-zg9lbn. Issued alongside the list rather than cached,
  // because it is a few hundred bytes next to a ~1.3MB board fetch and a
  // cache keyed on nothing in particular is the next stale-copy bug.
  // Skipped entirely when the caller wants everything: then there is nothing
  // to hide and a policy outage must not blank the board.
  const [list, policy] = await Promise.all([
    options.signal === undefined
      ? supervisorApi().listBeads(cityName, baseQuery)
      : supervisorApi().listBeads(cityName, baseQuery, options.signal),
    includeBookkeeping ? Promise.resolve(undefined) : supervisorApi().beadLabelPolicy(cityName),
  ]);
  const items = uniqueById(list.items ?? []);
  const statusFiltered = includeClosed ? items : items.filter((bead) => bead.status !== 'closed');
  const hiddenLabels = new Set(policy?.hidden_labels ?? []);
  const filtered = includeBookkeeping
    ? statusFiltered
    : statusFiltered.filter((bead) => isWorkBead(bead, hiddenLabels));
  const upstreamTotal = countAsNumber(list.total);
  return {
    items: filtered,
    total: filtered.length,
    ...(upstreamTotal === undefined ? {} : { upstream_total: upstreamTotal }),
    upstream_fetched: items.length,
    fetch_limit: limit,
  };
}

export async function listSupervisorBeadsAssignedTo(
  assignees: readonly string[],
  options: Pick<ListSupervisorBeadsOptions, 'includeClosed' | 'limit'> = {},
): Promise<SupervisorBeadList> {
  const cityName = activeCityOrThrow('list supervisor assigned beads');
  const uniqueAssignees = uniqueNonEmpty(assignees);
  const limit = options.limit ?? ASSIGNED_BEADS_FETCH_LIMIT;
  const includeClosed = options.includeClosed ?? false;
  if (uniqueAssignees.length === 0) {
    return {
      items: [],
      total: 0,
      upstream_fetched: 0,
      fetch_limit: limit,
    };
  }
  const lists = await Promise.all(
    uniqueAssignees.map((assignee) =>
      supervisorApi().listBeads(cityName, {
        assignee,
        limit,
        ...(includeClosed ? { all: true } : {}),
      }),
    ),
  );
  const items = uniqueById(lists.flatMap((list) => list.items ?? []));
  const upstreamTotal = sumTotals(lists);
  return {
    items,
    total: items.length,
    ...(upstreamTotal === undefined ? {} : { upstream_total: upstreamTotal }),
    upstream_fetched: items.length,
    fetch_limit: limit,
  };
}

export async function fetchSupervisorBead(id: string): Promise<SupervisorBead> {
  const cityName = activeCityOrThrow('fetch supervisor bead');
  try {
    return await supervisorApi().getBead(cityName, id);
  } catch (err) {
    if (!(err instanceof SupervisorApiError) || err.status !== 404) throw err;

    const list = await supervisorApi().listBeads(cityName, {
      limit: DETAIL_FALLBACK_FETCH_LIMIT,
    });
    const hit = (list.items ?? []).find((bead) => bead.id === id);
    if (hit !== undefined) return hit;
    throw err;
  }
}

// isWorkBead: does this row belong on a board of engineering work?
//
// Two dimensions, and the label one is a UNION of two rules rather than one.
// `hiddenLabels` is the supervisor's own ready-exclusion set and is the
// authority -- the set contains a family carrying no `gc:` prefix at all, and
// nothing but the served set catches it. The prefix test stays beside it as a
// forward-looking catch-all for a gc-internal row whose family nobody has
// added to that set yet; it is a heuristic, NOT a copy of the enumeration,
// which is why it does not name a single family.
function isWorkBead(bead: SupervisorBead, hiddenLabels: ReadonlySet<string>): boolean {
  if (!ENGINEERING_BEAD_TYPES.has(bead.issue_type)) return false;
  if (!Array.isArray(bead.labels)) return true;
  return !bead.labels.some((label) => hiddenLabels.has(label) || label.startsWith('gc:'));
}

function countAsNumber(value: ListBodyBead['total']): number | undefined {
  if (typeof value === 'number') return value;
  if (typeof value === 'bigint') return Number(value);
  return undefined;
}

function sumTotals(lists: readonly ListBodyBead[]): number | undefined {
  let total = 0;
  for (const list of lists) {
    const value = countAsNumber(list.total);
    if (value === undefined) return undefined;
    total += value;
  }
  return total;
}

function uniqueById(items: readonly SupervisorBead[]): SupervisorBead[] {
  const seen = new Set<string>();
  const unique: SupervisorBead[] = [];
  for (const item of items) {
    if (seen.has(item.id)) continue;
    seen.add(item.id);
    unique.push(item);
  }
  return unique;
}

function uniqueNonEmpty(values: readonly string[]): string[] {
  const seen = new Set<string>();
  const unique: string[] = [];
  for (const value of values) {
    const trimmed = value.trim();
    if (trimmed.length === 0 || seen.has(trimmed)) continue;
    seen.add(trimmed);
    unique.push(trimmed);
  }
  return unique;
}
