// Playwright config for the panel UI tests. The panel under test is the one
// served by the e2e demo (or any panel reachable at PANEL_URL); this config
// never starts it.
const { defineConfig, devices } = require('@playwright/test');

const PANEL_URL = process.env.PANEL_URL || 'http://127.0.0.1:18081';

module.exports = defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [['list']],
  timeout: 60_000,
  expect: { timeout: 15_000 },
  use: {
    baseURL: PANEL_URL,
    ...devices['Desktop Chrome'],
  },
});
