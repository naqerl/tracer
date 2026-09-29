// Loading the collector apps grid must not move the page: the skeleton cards
// have to occupy exactly the same boxes as the real cards that replace them.
const { test, expect } = require('@playwright/test');

const PANEL_USER = process.env.DEMO_USER || 'demo';
const PANEL_PASSWORD = process.env.DEMO_PASSWORD || 'demo1234';

// Sub-pixel layout rounding is the only difference tolerated between the
// skeleton and the loaded cards.
const TOLERANCE_PX = 0.5;

// Apps are laid out in one, two or three columns depending on the viewport,
// so the size of a card changes with it. The grid must stay pixel-perfect in
// every layout.
const VIEWPORTS = [
  { name: 'one column', width: 480, height: 900 },
  { name: 'two columns', width: 800, height: 900 },
  { name: 'three columns', width: 1280, height: 900 },
];

// Holds the apps-grid fragment so the skeleton stays on screen until the test
// measured it, then lets the request through.
async function holdAppsFragment(page) {
  let release;
  const held = new Promise((resolve) => {
    release = resolve;
  });
  let heldCount = 0;
  await page.route('**/panel/**', async (route) => {
    const request = route.request();
    const isAppsFragment =
      request.headers()['hx-request'] === 'true' && /\/panel\/collectors\/[^/]+\/apps(\?|$)/.test(request.url());
    if (!isAppsFragment) {
      await route.continue();
      return;
    }
    heldCount += 1;
    await held;
    await route.continue();
  });
  return {
    release,
    heldCount: () => heldCount,
  };
}

// Geometry of the first apps grid on the page: every card box, the boxes of
// the two text lines and of the bars row inside each card, and the position of
// a sentinel element injected right below the grid.
function measureGrids() {
  const box = (el) => {
    const r = el.getBoundingClientRect();
    return {
      x: Math.round(r.x * 100) / 100,
      y: Math.round(r.y * 100) / 100,
      width: Math.round(r.width * 100) / 100,
      height: Math.round(r.height * 100) / 100,
    };
  };
  const sentinel = document.getElementById('below-apps-grid');
  return Array.from(document.querySelectorAll('.apps-grid')).map((grid) => ({
    grid: box(grid),
    sentinel: sentinel ? box(sentinel) : null,
    documentHeight: document.documentElement.scrollHeight,
    cards: Array.from(grid.children).map((card) => {
      const lines = card.firstElementChild;
      return {
        card: box(card),
        lines: box(lines),
        name: box(lines.firstElementChild),
        meta: box(lines.children[1]),
        bars: box(card.lastElementChild),
        barCount: card.querySelectorAll('.uptime-bar, [class*="rounded-full"]').length,
      };
    }),
  }));
}

// Injects a painted block right below the apps grid. Painted content is what
// Chrome's layout instability API tracks, so the sentinel both reports how far
// the page moved and turns that movement into a layout shift entry.
async function injectSentinel(page) {
  await page.evaluate(() => {
    const grid = document.querySelector('.apps-grid');
    if (grid.parentElement.querySelector('#below-apps-grid')) {
      return;
    }
    const sentinel = document.createElement('div');
    sentinel.id = 'below-apps-grid';
    sentinel.style.cssText = 'height: 120px; background: #333333';
    grid.parentElement.insertAdjacentElement('afterend', sentinel);
  });
  // Chrome ignores the layout shift of content that was added in the same
  // frame it moved in, so let the sentinel settle into a frame of its own.
  await page.waitForTimeout(300);
}

async function openDashboard(page) {
  await page.goto('/panel/');
  if (page.url().includes('/auth/login')) {
    await page.fill('input[name="username"]', PANEL_USER);
    await page.fill('input[name="password"]', PANEL_PASSWORD);
    await page.click('button[type="submit"]');
    await page.waitForURL((url) => !url.pathname.startsWith('/auth/'));
  }
  await expect(page.locator('.apps-grid').first()).toBeAttached();
}

function compare(what, skeleton, loaded) {
  const diffs = [];
  for (const key of ['x', 'y', 'width', 'height']) {
    const delta = Math.abs(skeleton[key] - loaded[key]);
    if (delta > TOLERANCE_PX) {
      diffs.push(`${what}.${key}: skeleton ${skeleton[key]}px vs loaded ${loaded[key]}px (${delta}px off)`);
    }
  }
  return diffs;
}

for (const viewport of VIEWPORTS) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('app card skeletons are pixel-perfect', async ({ page }) => {
      const held = await holdAppsFragment(page);
      await openDashboard(page);

      const skeletonGrid = page.locator('.apps-grid').first();
      await expect(skeletonGrid.locator('.animate-pulse').first()).toBeVisible();
      await injectSentinel(page);

      const skeleton = (await page.evaluate(measureGrids))[0];
      expect(held.heldCount(), 'the apps fragment was never held').toBeGreaterThan(0);
      expect(skeleton.cards.length, 'panel renders no app cards to compare').toBeGreaterThan(0);

      held.release();
      await expect(skeletonGrid.locator('a').first()).toBeVisible();
      await expect(page.locator('#below-apps-grid')).toBeAttached();
      await page.waitForFunction(() => document.querySelectorAll('.apps-grid .animate-pulse').length === 0);

      const loaded = (await page.evaluate(measureGrids))[0];

      expect(loaded.cards.length).toBe(skeleton.cards.length);
      const diffs = [
        ...compare('grid', skeleton.grid, loaded.grid),
        ...compare('content below the grid', skeleton.sentinel, loaded.sentinel),
      ];
      skeleton.cards.forEach((card, i) => {
        const other = loaded.cards[i];
        diffs.push(...compare(`card ${i}`, card.card, other.card));
        diffs.push(...compare(`card ${i} text block`, card.lines, other.lines));
        diffs.push(...compare(`card ${i} name line`, card.name, other.name));
        diffs.push(...compare(`card ${i} meta line`, card.meta, other.meta));
        diffs.push(...compare(`card ${i} bars row`, card.bars, other.bars));
        if (card.barCount !== other.barCount) {
          diffs.push(`card ${i} bar count: skeleton ${card.barCount} vs loaded ${other.barCount}`);
        }
      });
      expect(diffs).toEqual([]);
      expect(
        Math.abs(loaded.documentHeight - skeleton.documentHeight),
        'page height changed while the apps grid loaded'
      ).toBeLessThanOrEqual(TOLERANCE_PX);
    });

    test('loading the apps grid causes no layout shift', async ({ page }) => {
      await page.addInitScript(() => {
        window.__cumulativeLayoutShift = 0;
        new PerformanceObserver((list) => {
          for (const entry of list.getEntries()) {
            if (!entry.hadRecentInput) {
              window.__cumulativeLayoutShift += entry.value;
            }
          }
        }).observe({ type: 'layout-shift', buffered: true });
      });

      const held = await holdAppsFragment(page);
      await openDashboard(page);
      const skeletonGrid = page.locator('.apps-grid').first();
      await expect(skeletonGrid.locator('.animate-pulse').first()).toBeVisible();
      await injectSentinel(page);
      await page.evaluate(() => {
        window.__cumulativeLayoutShift = 0;
      });
      const before = await page.evaluate(() => document.getElementById('below-apps-grid').getBoundingClientRect().y);

      held.release();
      await expect(skeletonGrid.locator('a').first()).toBeVisible();
      await page.waitForFunction(() => document.querySelectorAll('.apps-grid .animate-pulse').length === 0);
      await page.waitForTimeout(500);

      const after = await page.evaluate(() => document.getElementById('below-apps-grid').getBoundingClientRect().y);
      const shift = await page.evaluate(() => window.__cumulativeLayoutShift);
      expect(
        Math.abs(after - before),
        'content below the apps grid moved while it loaded'
      ).toBeLessThanOrEqual(TOLERANCE_PX);
      expect(shift, 'layout shift while the apps grid loaded').toBe(0);
    });
  });
}
