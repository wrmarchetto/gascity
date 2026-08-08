import { describe, it, expect } from 'vitest';
import { ALL_VIEWS } from './registry';

describe('views/registry', () => {
  it('contains the health view', () => {
    const ids = ALL_VIEWS.map((v) => v.id);
    expect(ids).toContain('health');
  });

  // Core, not firstParty: dashboardDeps leaves EnabledModules unset, so a
  // firstParty descriptor would be filtered out of every deployment and the
  // route would never mount. The kind assertion is the guard against a future
  // edit "tidying" it into a gated module and silently removing the tab.
  it('contains the accounts view as a core route', () => {
    const accounts = ALL_VIEWS.find((v) => v.id === 'accounts');
    expect(accounts).toBeDefined();
    expect(accounts?.kind).toBe('core');
    expect(accounts?.path).toBe('/accounts');
    expect(accounts?.nav).not.toBeNull();
    expect(accounts?.nav?.label).toBe('Accounts');
  });

  it('contains the activity view as a core route', () => {
    const activity = ALL_VIEWS.find((v) => v.id === 'activity');
    expect(activity).toBeDefined();
    expect(activity?.kind).toBe('core');
    expect(activity?.path).toBe('/activity');
    expect(activity?.nav).not.toBeNull();
    expect(activity?.nav?.label).toBe('Activity');
  });

  it('has no duplicate ids', () => {
    const ids = ALL_VIEWS.map((v) => v.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('has no duplicate paths', () => {
    const paths = ALL_VIEWS.map((v) => v.path);
    expect(new Set(paths).size).toBe(paths.length);
  });

  it('healthView is core, mounts at /health, and has a Health nav entry', () => {
    const health = ALL_VIEWS.find((v) => v.id === 'health');
    expect(health).toBeDefined();
    expect(health?.kind).toBe('core');
    expect(health?.path).toBe('/health');
    expect(health?.nav).not.toBeNull();
    expect(health?.nav?.label).toBe('Health');
  });

  it('every view exposes a renderable element (React.lazy result)', () => {
    for (const v of ALL_VIEWS) {
      // React.lazy returns an exotic object with a $$typeof symbol and a
      // _payload. We assert structurally — anything else would mean the
      // module file forgot to wrap in lazy() and would ship in the
      // default-paint bundle.
      expect(typeof v.element).toBe('object');
      expect(v.element).not.toBeNull();
    }
  });
});
