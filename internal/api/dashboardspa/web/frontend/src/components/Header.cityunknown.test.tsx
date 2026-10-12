import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AttentionProvider } from '../attention/context';
import { ThemeProvider } from '../contexts/ThemeContext';
import { ViewingAsProvider } from '../contexts/ViewingAsContext';
import { Header } from './Header';

/**
 * Scope: what the city switcher shows while the city LIST is still in
 * flight. `(unknown)` is the header's way of saying the URL names a city the
 * backend does not have -- a real, actionable fault. It must never be how
 * the header says "I have not finished loading".
 *
 * Why the suite exists: the distinction is invisible to every other test
 * here. Header.navlinks.test.tsx resolves `listCities` immediately, so its
 * renders skip the pre-load window entirely, and the browser e2e asserts
 * after the page settles -- by which time the list has landed and the wrong
 * label is gone. The window is real and it is the operator's first paint:
 * measured 2026-10-12 against the live dashboard, the header read
 * `city (unknown)` with no Summaries entry at 159ms and the correct name at
 * 211ms, on a load with nothing else wrong with it. Under any pressure that
 * delays /v0/cities, that transient becomes the steady state.
 *
 * The test holds `listCities` unresolved for the whole render rather than
 * resolving it late, because a late resolve pins only the settled frame --
 * the same blind spot the existing suites have.
 *
 * Run: npm test -- Header.cityunknown  (from internal/api/dashboardspa/web/frontend)
 */

const CITY = 'test-city';

vi.mock('../api/client', () => ({
  api: {
    config: () =>
      Promise.resolve({
        cityName: CITY,
        defaultView: null,
        enabledModules: [],
        readOnly: false,
        navLinks: [],
      }),
  },
}));

// Never settles. A pending promise is the pre-load state as the component
// actually sees it; a mock that resolves with an empty list would instead
// assert the OPPOSITE invariant -- a loaded list that omits the city, which
// is the genuine `(unknown)` case and must keep working.
vi.mock('../supervisor/client', () => ({
  supervisorApi: () => ({
    listCities: () => new Promise(() => {}),
  }),
}));

function renderHeader() {
  return render(
    <ThemeProvider>
      <ViewingAsProvider>
        <AttentionProvider contributors={[]}>
          <MemoryRouter future={{ v7_relativeSplatPath: true, v7_startTransition: true }}>
            <Header />
          </MemoryRouter>
        </AttentionProvider>
      </ViewingAsProvider>
    </ThemeProvider>,
  );
}

// ThemeProvider reads window.matchMedia during its first render and jsdom
// does not implement it. Mirrors the stub in Header.navlinks.test.tsx.
beforeEach(() => {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      addEventListener: vi.fn(),
      addListener: vi.fn(),
      dispatchEvent: vi.fn(),
      matches: false,
      media: query,
      onchange: null,
      removeEventListener: vi.fn(),
      removeListener: vi.fn(),
    })),
  });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('Header city label while the city list is in flight', () => {
  it('does not call the active city unknown before the list has loaded', async () => {
    renderHeader();

    // Wait for the config query to land, so `selectedCity` is populated and
    // the component is in the exact state the invariant is about: a known
    // selected city, an unresolved list. Asserting before this would pass
    // vacuously -- `selectedCity === ''` short-circuits activeCityKnown to
    // true and nothing could render `(unknown)` yet.
    await waitFor(() => {
      expect(screen.getByText(new RegExp(CITY))).toBeTruthy();
    });

    expect(screen.queryByText(`${CITY} (unknown)`)).toBeNull();
  });
});
