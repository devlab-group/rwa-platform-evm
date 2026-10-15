import { defineConfig, devices } from "@playwright/test";

// BASE_URL selects the test mode:
// - Unset: vite preview serves the production build; API and wallet mocks need no
//   backend and keep tests deterministic.
// - Set (http://host:port): use a running SPA/API deployment without mocks; skip
//   mockOnly() specs.
// See tests/fixtures/fixtures.ts.
const liveBaseUrl = process.env.BASE_URL;
const PREVIEW_PORT = 4173;

export default defineConfig({
  testDir: "./tests",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["line"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: liveBaseUrl ?? `http://localhost:${PREVIEW_PORT}`,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  // Start a local static server only in mocked mode; with a live BASE_URL the
  // target is managed externally
  webServer: liveBaseUrl
    ? undefined
    : {
        command: `npm run preview -- --port ${PREVIEW_PORT} --strictPort`,
        url: `http://localhost:${PREVIEW_PORT}`,
        reuseExistingServer: !process.env.CI,
        timeout: 30_000,
      },
});
