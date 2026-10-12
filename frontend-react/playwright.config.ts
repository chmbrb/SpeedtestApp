import { defineConfig } from '@playwright/test'

// The e2e tests drive a real browser against a running stack: the Go backend serving this build
// (STATIC_DIR=frontend-react/dist) on a database that has data in it. Point E2E_BASE_URL at it.
export default defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  expect: { timeout: 8_000 },
  fullyParallel: true,
  reporter: [['list']],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8083',
    timezoneId: 'America/Phoenix',
    locale: 'en-US',
    // For running against the HTTPS stack, whose certificate comes from its own private authority.
    ignoreHTTPSErrors: !!process.env.E2E_IGNORE_HTTPS_ERRORS,
    launchOptions: { args: process.env.E2E_IGNORE_HTTPS_ERRORS ? ['--ignore-certificate-errors'] : [] }, // service workers need this too
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
})
