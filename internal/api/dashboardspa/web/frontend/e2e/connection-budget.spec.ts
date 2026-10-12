import type { Page } from '@playwright/test';

import { CITY_BASE } from './fixtures/expected';
import { gotoCityRoute } from './support/renderGuards';
import { expect, test } from './support/fixtures';

// Scope: the dashboard keeps serving its own data when an operator leaves
// several of its pages open on ONE browser profile.
//
// The defect this pins (bead ci-s4mwuy): each dashboard page held its own
// EventSource to the city event stream for the page's whole life, and a
// browser gives an HTTP/1.1 origin six sockets. Six open pages consumed all
// six, and every request issued after that -- in every page, including one
// just opened -- was QUEUED IN THE BROWSER and never sent. Measured
// 2026-10-12 against the live dashboard: at six pages, usage, status,
// runs/census, sessions and runs/summary all carried CDP connectionId=null;
// at seven, the HTML document itself could not get a socket. The server
// answers each of those endpoints in microseconds and logs nothing, so
// nothing server-side can see this.
//
// Why it asserts on the mail lamp and NOT on the navbar: starvation takes a
// SUFFIX of the load, not a random subset. React runs effects child-first, so
// the issue order is fixed -- document, cities, config, attention batch,
// cockpit panels, and the city event stream among the last -- and only
// requests after a page's own stream can be starved. /config and /v0/cities
// are always ahead of it, so the navbar and the city bootstrap are intact on
// a starved page. An assertion on either would pass at six pages and pin
// nothing. The lamps are rendered from /v0/city/{city}/status, which is in
// the starved set.
//
// What this spec CANNOT represent, so it does not retire the manual check:
// every Playwright page reports document.visibilityState === 'visible'
// however many are open and whatever is brought to front, headless and headed
// alike (measured 2026-10-12, as are Emulation.setFocusEmulationEnabled and
// Page.setWebLifecycleState). A real browser hides background tabs. So a fix
// that released the stream only on `visibilitychange` would still fail here,
// and a fix that passes here holds for VISIBLE pages -- the stricter
// condition. It also cannot represent HTTP/2, where the six-socket budget
// does not apply; the supervisor serves HTTP/1.1 cleartext, which is the
// deployed shape.
//
// Run: npx playwright test connection-budget   (from this frontend workspace)

// The number of sockets Chromium allows an HTTP/1.1 origin. At exactly this
// many pages the last page's own stream takes the final socket and its
// remaining reads are starved; one more and the document request itself is.
// Named rather than inlined because the count IS the invariant.
const PAGES = 6;

// The seeded corpus's unread mail count, as it reaches the systems section's
// mail lamp. Asserted because it is a value the page CONSUMED: it can only
// render if /v0/city/{city}/status arrived and was read. A starved page
// satisfies "issued no failed request" trivially, by never issuing one, so
// an assertion on request outcomes would pass over the whole defect. Mirrors
// render-smoke.spec.ts's single-page assertion on the same lamp.
const SEEDED_MAIL_LAMP = /3 unread/;

test.describe('connection budget across open dashboard pages', () => {
  // Six sequential page loads plus the assertions do not fit the suite's 30s
  // default, and a budget tuned to the PASSING path keeps the red run honest:
  // without it the spec would also fail on a fixed build, for time.
  test.setTimeout(120_000);

  test(`page ${PAGES} still completes its city-scoped reads`, async ({ browser }) => {
    // Its own context, not the `page` fixture: Chromium pools sockets per
    // browser context, so the pages must share ONE context to contend for
    // them. Two contexts would starve nothing and the spec would be green
    // over the defect.
    const context = await browser.newContext();
    try {
      const pages: Page[] = [];
      for (let i = 0; i < PAGES; i += 1) {
        const page = await context.newPage();
        pages.push(page);
        await gotoCityRoute(page, CITY_BASE, '');
      }
      const last = pages[pages.length - 1]!;

      await expect(
        last.getByRole('region', { name: 'systems' }).getByRole('link', { name: SEEDED_MAIL_LAMP }),
        `page ${PAGES} never rendered the seeded mail lamp: its /status read was starved of a socket`,
      ).toBeVisible();
      // The same lamps' unavailable branch, asserted separately so a future
      // rename of the seeded value cannot turn this spec into a vacuous pass.
      await expect(
        last.getByRole('region', { name: 'systems' }).getByRole('link', { name: /unavailable/ }),
      ).toHaveCount(0);
    } finally {
      await context.close();
    }
  });
});
