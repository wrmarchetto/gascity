import { CITY_BASE, NAV_LINK_HREF, NAV_LINK_LABEL } from './fixtures/expected';
import { gotoCityRoute } from './support/renderGuards';
import { expect, test } from './support/fixtures';

// Layer B coverage for the operator-declared navbar entry (DASHBOARD_NAV_LINKS
// -> internal/api/dashboardbff/navlinks.go -> Header.tsx). The fake supervisor
// is launched with the real env var (playwright.config.ts webServer.env), so
// this drives the whole path in a real browser.
//
// Why it exists when Go and jsdom suites already cover the same code: neither
// can represent the three things that actually decide whether the operator has
// a working link. A jsdom render proves an <a> is in the tree, not that it is
// VISIBLE -- a navbar that overflows or a parent with `hidden` leaves the
// element present and unreachable. It cannot prove the anchor performs a REAL
// navigation rather than being swallowed by the SPA router, because jsdom
// performs no navigation at all. And it cannot prove the entry survives the
// BUILT bundle, which is the artifact the dashboard actually serves.
//
// Run: npx playwright test navbar-navlinks  (from this frontend workspace)

test.describe('operator-declared navbar entry', () => {
  test('renders in the live navbar and is visible', async ({ page }) => {
    await gotoCityRoute(page, CITY_BASE, '');

    const link = page.getByRole('navigation').getByRole('link', { name: NAV_LINK_LABEL });
    await expect(link).toBeVisible();
    await expect(link).toHaveAttribute('href', NAV_LINK_HREF);
  });

  test('sits after the SPA routes rather than among them', async ({ page }) => {
    await gotoCityRoute(page, CITY_BASE, '');

    const labels = await page.getByRole('navigation').getByRole('link').allTextContents();
    expect(labels[labels.length - 1]?.trim()).toBe(NAV_LINK_LABEL);
    expect(labels.length).toBeGreaterThan(1);
  });

  // The behavior no non-browser layer can reach. A react-router NavLink would
  // keep the SPA mounted and swap in the router's own no-match view, leaving
  // the address bar on a /city/... path. A real navigation leaves the SPA.
  test('clicking it performs a real navigation off the SPA', async ({ page }) => {
    await gotoCityRoute(page, CITY_BASE, '');

    const link = page.getByRole('navigation').getByRole('link', { name: NAV_LINK_LABEL });
    await Promise.all([page.waitForURL(`**${NAV_LINK_HREF}`), link.click()]);

    expect(new URL(page.url()).pathname).toBe(NAV_LINK_HREF);
    // The SPA's own navbar must be gone. Asserting on the URL alone would
    // pass for a router that pushed the path client-side without unmounting,
    // which is precisely the failure this test exists to catch.
    await expect(page.getByRole('navigation').getByRole('link', { name: 'Beads' })).toHaveCount(0);
    // Deliberately NOT also asserting the URL no longer contains CITY_BASE:
    // the href itself is /v0/city/<city>/svc/..., so CITY_BASE
    // (/city/<city>) is a substring of the destination and the check fails
    // on a correct navigation. The unmounted navbar above is the real signal.
  });
});
