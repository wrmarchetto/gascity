import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setActiveCity } from '../api/cityBase';
import type { Bead } from 'gas-city-dashboard-shared/gc-supervisor';
import {
  GC_MUTATION_HEADERS,
  resetSupervisorApiForTests,
  setSupervisorApiForTests,
  SupervisorApiError,
  type SupervisorApi,
} from './client';
import { fetchSupervisorBead, listSupervisorBeads } from './beadReads';

const baseApi: SupervisorApi = {
  baseUrl: '/gc-supervisor',
  health: vi.fn(),
  cityHealth: vi.fn(),
  cityStatus: vi.fn(),
  cityUsage: vi.fn(),
  runCensus: vi.fn(),
  listCities: vi.fn(),
  listAgents: vi.fn(),
  listRigs: vi.fn(),
  listBeads: vi.fn(),
  listEvents: vi.fn(),
  // Refuses rather than answering, so a test that reaches the label policy
  // without scripting it FAILS instead of silently filtering on an empty set
  // -- which is indistinguishable from "nothing was hidden".
  beadLabelPolicy: vi.fn(async () => {
    throw new Error('beadLabelPolicy called without being scripted by this test');
  }),
  getBead: vi.fn(),
  createBead: vi.fn(),
  updateBead: vi.fn(),
  closeBead: vi.fn(),
  sling: vi.fn(),
  formulaFeed: vi.fn(),
  listMail: vi.fn(),
  markMailRead: vi.fn(),
  markMailUnread: vi.fn(),
  archiveMail: vi.fn(),
  replyMail: vi.fn(),
  sendMail: vi.fn(),
  mailThread: vi.fn(),
  cityEventStreamUrl: vi.fn(),
  sessionStreamUrl: vi.fn(),
  listSessions: vi.fn(),
  sessionPending: vi.fn(),
  respondSession: vi.fn(),
  sessionTranscript: vi.fn(),
  workflowRun: vi.fn(),
  formulaDetail: vi.fn(),
  mutationHeaders: () => ({ ...GC_MUTATION_HEADERS }),
};

describe('supervisor bead reads', () => {
  beforeEach(() => {
    setActiveCity('test-city');
  });

  afterEach(() => {
    resetSupervisorApiForTests();
  });

  it('keeps decision beads in the default work queue while excluding bookkeeping/system rows', async () => {
    const listBeads = vi.fn(async () => ({
      items: [
        bead({ id: 'rc-decision', issue_type: 'decision' }),
        bead({ id: 'td-task', issue_type: 'task' }),
        bead({ id: 'sys-session', issue_type: 'session' }),
        bead({ id: 'gc-task', issue_type: 'task', labels: ['gc:internal'] }),
      ],
      total: 4,
    }));
    setSupervisorApiForTests({ ...baseApi, listBeads, beadLabelPolicy: policy() });

    const result = await listSupervisorBeads();

    expect(listBeads).toHaveBeenCalledWith('test-city', { limit: 1000 });
    expect(result.items.map((item) => item.id)).toEqual(['rc-decision', 'td-task']);
    expect(result.total).toBe(2);
    expect(result.upstream_total).toBe(4);
  });

  // ci-zg9lbn. The operator's board was 101 of 118 rows of Slack transcript.
  // The label that hides them is served by the supervisor, never typed here:
  // the fixture below feeds one in through the policy and the row must vanish
  // on that basis alone -- its issue_type is `task`, the same type real work
  // carries, so no type rule can be what dropped it.
  it('hides a row whose label the supervisor reports as bookkeeping', async () => {
    const listBeads = vi.fn(async () => ({
      items: [
        bead({ id: 'td-work' }),
        bead({ id: 'td-transcript', labels: ['served-hidden-label'] }),
      ],
      total: 2,
    }));
    setSupervisorApiForTests({
      ...baseApi,
      listBeads,
      beadLabelPolicy: policy(['served-hidden-label']),
    });

    const result = await listSupervisorBeads();

    expect(result.items.map((item) => item.id)).toEqual(['td-work']);
  });

  // The hidden set is the SERVED one, not a prefix guess. `order-tracking`
  // carries no `gc:` prefix and is in gc's ready-exclusion list, so a board
  // filtering on the prefix alone would show it -- which is the rot this read
  // exists to prevent.
  it('hides a served bookkeeping label that carries no gc: prefix', async () => {
    const listBeads = vi.fn(async () => ({
      items: [bead({ id: 'td-order', labels: ['unprefixed-served-label'] })],
      total: 1,
    }));
    setSupervisorApiForTests({
      ...baseApi,
      listBeads,
      beadLabelPolicy: policy(['unprefixed-served-label']),
    });

    const result = await listSupervisorBeads();

    expect(result.items).toEqual([]);
  });

  it('reveals bookkeeping rows, and skips the policy read, when asked to include them', async () => {
    const listBeads = vi.fn(async () => ({
      items: [
        bead({ id: 'td-work' }),
        bead({ id: 'td-transcript', labels: ['served-hidden-label'] }),
        bead({ id: 'sys-session', issue_type: 'session' }),
      ],
      total: 3,
    }));
    // Left at the refusing stand-in on purpose: asking for everything must not
    // need the policy at all, and if the code reads it anyway this test dies.
    setSupervisorApiForTests({ ...baseApi, listBeads });

    const result = await listSupervisorBeads({ includeBookkeeping: true });

    expect(result.items.map((item) => item.id)).toEqual([
      'td-work',
      'td-transcript',
      'sys-session',
    ]);
  });

  // Do not swallow the policy read's failure into "hide nothing" or "hide
  // everything": either one silently misreports the board. The caller renders
  // the error instead.
  it('propagates a failed label-policy read instead of guessing a hidden set', async () => {
    const listBeads = vi.fn(async () => ({ items: [bead({ id: 'td-work' })], total: 1 }));
    const beadLabelPolicy = vi.fn(async () => {
      throw new SupervisorApiError(503, 'supervisor unavailable', undefined);
    });
    setSupervisorApiForTests({ ...baseApi, listBeads, beadLabelPolicy });

    await expect(listSupervisorBeads()).rejects.toMatchObject({ status: 503 });
  });

  it('uses an explicit city instead of re-reading the active city', async () => {
    const listBeads = vi.fn(async () => ({ items: [], total: 0 }));
    setSupervisorApiForTests({ ...baseApi, listBeads, beadLabelPolicy: policy() });

    await listSupervisorBeads({ city: 'captured-city' });

    expect(listBeads).toHaveBeenCalledWith('captured-city', { limit: 1000 });
  });

  // gascity-dashboard-sg9o: a "needs you" decision alert can deep-link to a
  // bead the supervisor has since pruned (e.g. gc-316879). fetchSupervisorBead
  // is the data edge the deep-link modal sits on: it must surface a true 404 as
  // a SupervisorApiError(404) so useBeadDetail can render the calm "resolved or
  // removed" state instead of a hard error.
  it('re-raises a 404 when a deep-linked bead is gone and absent from the fallback list', async () => {
    const getBead = vi.fn(async () => {
      throw new SupervisorApiError(404, 'bead missing', undefined);
    });
    const listBeads = vi.fn(async () => ({ items: [bead({ id: 'td-other' })], total: 1 }));
    setSupervisorApiForTests({ ...baseApi, getBead, listBeads });

    await expect(fetchSupervisorBead('gc-316879')).rejects.toMatchObject({
      name: 'SupervisorApiError',
      status: 404,
    });
    expect(getBead).toHaveBeenCalledWith('test-city', 'gc-316879');
  });

  it('recovers a deep-linked bead from the fallback list when getBead 404s but it still lists', async () => {
    const getBead = vi.fn(async () => {
      throw new SupervisorApiError(404, 'bead missing', undefined);
    });
    const listBeads = vi.fn(async () => ({
      items: [bead({ id: 'rc-decision', issue_type: 'decision' })],
      total: 1,
    }));
    setSupervisorApiForTests({ ...baseApi, getBead, listBeads });

    const hit = await fetchSupervisorBead('rc-decision');

    expect(hit.id).toBe('rc-decision');
  });

  it('propagates non-404 read failures without consulting the fallback list', async () => {
    const getBead = vi.fn(async () => {
      throw new SupervisorApiError(503, 'supervisor unavailable', undefined);
    });
    const listBeads = vi.fn();
    setSupervisorApiForTests({ ...baseApi, getBead, listBeads });

    await expect(fetchSupervisorBead('rc-decision')).rejects.toMatchObject({ status: 503 });
    expect(listBeads).not.toHaveBeenCalled();
  });
});

// A scripted label policy. The labels are invented strings, never gc's real
// ones: a fixture that named a real family would pass even if the code had
// stopped reading the served set and gone back to a hardcoded list.
function policy(hiddenLabels: readonly string[] = []) {
  return vi.fn(async () => ({
    hidden_labels: [...hiddenLabels],
    hidden_types: ['session', 'message'],
  }));
}

function bead(overrides: Partial<Bead>): Bead {
  return {
    id: 'td-default',
    issue_type: 'task',
    title: 'Default bead',
    status: 'open',
    created_at: '2026-06-01T00:00:00Z',
    ...overrides,
  };
}
