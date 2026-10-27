import { defineConfig, devices } from '@playwright/test'

// Tests run against a real `pkgops serve` with a seeded, signed repository
// (e2e/fixtures/seed.mjs). They share that one server, so they run serially.
// Specs read the server address from e2e/env.ts after global setup.
export default defineConfig({
  testDir: 'e2e',
  outputDir: 'e2e/.results',
  globalSetup: './e2e/global-setup.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  maxFailures: 3, // a broken setup should cost seconds, not one timeout per test
  timeout: 30_000,
  reporter: [['list']],
  use: { testIdAttribute: 'data-test', trace: 'retain-on-failure' },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 }, colorScheme: 'light' } },
    { name: 'dark', use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 }, colorScheme: 'dark' }, testMatch: /(a11y|screenshots)\.spec\.ts/ },
    { name: 'phone', use: { ...devices['Pixel 5'], colorScheme: 'light' }, testMatch: /(a11y|screenshots|layout|manage)\.spec\.ts/ },
  ],
})
