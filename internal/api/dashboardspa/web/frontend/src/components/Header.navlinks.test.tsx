import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AttentionProvider } from '../attention/context';
import { ThemeProvider } from '../contexts/ThemeContext';
import { ViewingAsProvider } from '../contexts/ViewingAsContext';
import { Header } from './Header';

/**
 * Scope: the operator-declared navbar entries (`navLinks`) the backend
 * projects from DASHBOARD_NAV_LINKS. The parsing and the refusal of an
 * unsafe href belong to the Go side and are pinned there
 * (internal/api/dashboardbff/navlinks_test.go) -- this suite covers only
 * what the Go side cannot see: that the entry reaches the DOM, that it is a
 * real anchor with a real href rather than a router route, and that it sits
 * after the SPA's own routes.
 *
 * What it cannot represent: the live page. A jsdom render proves the anchor
 * exists, not that it is visible, reachable or correctly placed in the real
 * navbar, so the browser check that accompanies this is not redundant.
 *
 * Run: npm test -- Header.navlinks  (from internal/api/dashboardspa/web/frontend)
 */

const config = vi.fn();

vi.mock('../api/client', () => ({
  api: { config: () => config() },
}));

vi.mock('../supervisor/client', () => ({
  supervisorApi: () => ({
    listCities: vi.fn(async () => ({
      items: [{ name: 'test-city', path: '/srv/test-city', running: true }],
      total: 1,
    })),
  }),
}));

function baseConfig(navLinks: ReadonlyArray<{ label: string; href: string }>) {
  return {
    cityName: 'test-city',
    defaultView: null,
    enabledModules: [],
    readOnly: false,
    navLinks,
  };
}

// Reports the router's current path so a click can be observed. A plain
// anchor leaves the router where it was (jsdom performs no navigation); a
// react-router NavLink moves it to the link's target. That difference is the
// only thing that distinguishes the two in a jsdom render -- both emit an
// <a> with the same href and, for an unrouted path, the same inactive
// classes, so an assertion on markup alone cannot tell them apart.
function LocationProbe() {
  return <output data-testid="pathname">{useLocation().pathname}</output>;
}

function renderHeader() {
  return render(
    <ThemeProvider>
      <ViewingAsProvider>
        <AttentionProvider contributors={[]}>
          <MemoryRouter future={{ v7_relativeSplatPath: true, v7_startTransition: true }}>
            <Header />
            <LocationProbe />
          </MemoryRouter>
        </AttentionProvider>
      </ViewingAsProvider>
    </ThemeProvider>,
  );
}

describe('Header operator-declared nav links', () => {
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
    config.mockReset();
  });

  it('renders a declared link as an anchor carrying its href', async () => {
    config.mockResolvedValue(baseConfig([{ label: 'Summaries', href: '/svc/roadmap/summaries' }]));
    renderHeader();

    const link = await screen.findByRole('link', { name: 'Summaries' });
    // getAttribute, not .href: the DOM property resolves against the test
    // origin, so an assertion on it would pass for a broken relative href.
    expect(link.getAttribute('href')).toBe('/svc/roadmap/summaries');
  });

  // Established by mutation: swapping the anchor for a react-router NavLink
  // leaves every other test in this file green, including an assertion on
  // the rendered classes. Only observing the click does the mutation die.
  it('leaves a declared link outside the router so it performs a real navigation', async () => {
    config.mockResolvedValue(baseConfig([{ label: 'Summaries', href: '/svc/roadmap/summaries' }]));
    renderHeader();

    const link = await screen.findByRole('link', { name: 'Summaries' });
    const before = screen.getByTestId('pathname').textContent;
    // jsdom performs no real navigation, so the router staying put is the
    // signal. A NavLink would intercept this click and hand the path to the
    // SPA router, which has no /svc route and would render its own 404.
    fireEvent.click(link);
    expect(screen.getByTestId('pathname').textContent).toBe(before);
    expect(screen.getByTestId('pathname').textContent).not.toBe('/svc/roadmap/summaries');
  });

  it('places declared links after the SPA routes', async () => {
    config.mockResolvedValue(baseConfig([{ label: 'Summaries', href: '/svc/roadmap/summaries' }]));
    renderHeader();

    await screen.findByRole('link', { name: 'Summaries' });
    const labels = screen.getAllByRole('link').map((a) => a.textContent?.trim());
    expect(labels[labels.length - 1]).toBe('Summaries');
    expect(labels.indexOf('Home')).toBeLessThan(labels.indexOf('Summaries'));
  });

  it('preserves declaration order across several links', async () => {
    config.mockResolvedValue(
      baseConfig([
        { label: 'Summaries', href: '/svc/roadmap/summaries' },
        { label: 'Wiki', href: 'https://example.test/w' },
      ]),
    );
    renderHeader();

    await screen.findByRole('link', { name: 'Wiki' });
    const labels = screen.getAllByRole('link').map((a) => a.textContent?.trim());
    expect(labels.slice(-2)).toEqual(['Summaries', 'Wiki']);
  });

  // The default install declares none. An empty array must add nothing --
  // not a separator, not an empty <li> -- or every dashboard that never set
  // the var grows a blank cell in its navbar.
  //
  // Asserted against a baseline captured in the same test rather than an
  // enumerated label list: the registry's own entries are not this suite's
  // business, and a hand-kept list of them would go red on the next view
  // that ships, which is a failure about nothing.
  it('adds nothing when no link is declared', async () => {
    config.mockResolvedValue(baseConfig([]));
    renderHeader();
    await screen.findByRole('link', { name: 'Home' });
    const baseline = screen.getAllByRole('link').map((a) => a.textContent?.trim());
    cleanup();

    config.mockResolvedValue(baseConfig([{ label: 'Summaries', href: '/svc/roadmap/summaries' }]));
    renderHeader();
    await screen.findByRole('link', { name: 'Summaries' });
    const withLink = screen.getAllByRole('link').map((a) => a.textContent?.trim());

    expect(withLink).toEqual([...baseline, 'Summaries']);
  });

  // The field is absent on a backend older than this change, and absent for
  // the whole of the pre-load state. Neither may throw: the navbar is the
  // page's only means of navigation, so a render crash here takes the entire
  // dashboard, not one entry.
  it('survives a config that carries no navLinks field at all', async () => {
    config.mockResolvedValue({
      cityName: 'test-city',
      defaultView: null,
      enabledModules: [],
      readOnly: false,
    });
    renderHeader();

    await screen.findByRole('link', { name: 'Home' });
    expect(screen.queryByRole('link', { name: 'Summaries' })).toBeNull();
  });
});
