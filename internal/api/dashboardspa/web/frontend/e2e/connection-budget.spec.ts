import type { Page } from '@playwright/test';

import { CITY_BASE } from './fixtures/expected';
import { expect, test } from './support/fixtures';

// Scope: the dashboard keeps serving its own data when an operator leaves
// several of its pages open on ONE browser profile.
//
// DEFERRED -- this is `test.fixme` because the defect it pins is REAL and NOT
// FIXED. It is bead ci-s4mwuy, which carries the measurements and a candidate
// fix on branch fix/ci-s4mwuy-shared-event-stream. Un-fixme it there and
// watch it go red-to-green. It is committed red rather than withheld because
// the suite is otherwise completely green over the defect, and a reader of
// this directory would have no way to learn it exists.
//
// The defect: each dashboard page holds an EventSource to the city event
// stream for its whole life, and a browser gives an HTTP/1.1 origin six
// sockets. Six open pages consume all six, and every request issued after
// that -- in every page -- is QUEUED IN THE BROWSER and never sent. Measured
// 2026-10-12 against the live dashboard: at six pages, usage, status,
// runs/census, sessions and runs/summary all carried CDP connectionId=null;
// at seven, the HTML document itself could not get a socket. The server
// answers each of those endpoints in microseconds and logs nothing.
//
// Why it asserts on the dolt-store lamp and NOT on the navbar: starvation
// takes a SUFFIX of the load, not a random subset. React runs effects
// child-first, so the issue order is fixed -- document, cities, config,
// attention batch, cockpit panels, and the event stream LAST -- and only
// requests after the page's own stream can be starved. /config is always
// ahead of it, so the navbar is intact on a starved page. An assertion on the
// Summaries anchor would therefore pass at six pages and pin nothing. The
// lamp reads `unavailable` exactly when /v0/city/{city}/status never arrived
// (routes/CockpitHome.tsx:182-189), which is the starved set.
//
// What this spec CANNOT represent, so it does not retire the manual check:
// every Playwright page reports document.visibilityState === 'visible'
// however many are open and whatever is brought to front, headless and headed
// alike (measured 2026-10-12, as are Emulation.setFocusEmulationEnabled and
// Page.setWebLifecycleState). A real browser hides background tabs. So a fix
// that released the stream on `visibilitychange` would still fail here, and a
// fix that passes here holds for VISIBLE pages -- the stricter condition.
//
// Established as a control before being trusted: the same spec with PAGES=1
// passes in 1.4s and with PAGES=6 fails, against the same build. A spec that
// only ever went red would not have shown which of the two it was measuring.
//
// Run: npx playwright test connection-budget   (from this frontend workspace)

// The number of sockets Chromium allows an HTTP/1.1 origin. At exactly this
// many pages the last page's own stream takes the final socket and its
// remaining reads are starved; one more and the document request itself is.
// Named rather than inlined because the count IS the invariant.
const PAGES = 6;

/**
 * openCityHome opens one more page on the shared context, waits until it has
 * actually opened its event stream, and leaves it open.
 *
 * Waiting for the stream is the precondition, not a settle delay. The stream
 * is the LAST request a load issues, so without the wait the next page is
 * opened while the previous ones are still mid-load and holding no stream
 * yet: six pages then contend for nothing and the spec passes against the
 * defect (measured -- it did, in 2.0s). Waiting on the request is sound here
 * because it is establishing the condition under test, not asserting the
 * outcome; the assertions below stay on rendered values.
 */
async function openCityHome(page: Page): Promise<void> {
  const streamOpened = page.waitForRequest((request) => request.url().includes('/events/stream'));
  await page.goto(`${CITY_BASE}/`, { waitUntil: 'domcontentloaded' });
  await streamOpened;
}

test.describe('connection budget across open dashboard pages', () => {
  // Six sequential page loads plus the assertions do not fit the suite's 30s
  // default, and a budget tuned to the PASSING path keeps the red run honest:
  // without it the spec would also fail on a fixed build, for time.
  test.setTimeout(120_000);

  test.fixme(`page ${PAGES} still completes its city-scoped reads`, async ({ browser }) => {
    // Its own context, not the `page` fixture: sockets are pooled per browser
    // profile, so the pages must share one context to contend for them.
    const context = await browser.newContext();
    try {
      // The pages before the last exist only to hold their streams; the last
      // is the one under test, and it is opened with the pool already full.
      for (let i = 0; i < PAGES - 1; i += 1) {
        await openCityHome(await context.newPage());
      }
      const last = await context.newPage();
      await last.goto(`${CITY_BASE}/`, { waitUntil: 'domcontentloaded' });

      // Asserted on CONSUMED values -- what the page rendered -- never on
      // whether a request appeared. A starved page satisfies "no failed
      // request" trivially, by never issuing one.
      //
      // `loading usage…` is what the Home panels print while the usage read
      // has not arrived, and it is the one string that cleanly separates the
      // two populations. Measured in this rig 2026-10-12: a healthy page
      // shows `usage recording is not local` five times and `loading usage…`
      // never; a starved sixth page shows `loading usage…` five times.
      //
      // The dolt-store lamp is deliberately NOT asserted on. It reads
      // `unavailable` on the same condition, but the label and value render
      // as separate nodes, so getByText('dolt store unavailable') matches
      // NOTHING -- an earlier revision of this spec used it and passed
      // against a page that was fully starved.
      //
      // Both poles are asserted, not just the negative: a blank page has no
      // `loading usage…` either, so the absence alone would be satisfied by
      // the page failing to render at all.
      await expect(last.getByText('usage recording is not local').first()).toBeVisible();
      await expect(last.getByText('loading usage…')).toHaveCount(0);
    } finally {
      await context.close();
    }
  });
});
