import { defineConfig, devices } from "@playwright/test";

// Two modes, selected by whether BASE_URL is set:
//  - default (no BASE_URL): serves the production build with `vite preview`,
//    and every spec installs a mocked API and a mocked wallet (see
//    tests/fixtures). It needs no backend and is deterministic.
//  - live (BASE_URL=http://host:port): points at an already running
//    deployment that serves this SPA together with the platform API. Specs
//    install no mocks in this mode, and specs marked mockOnly() are skipped
//    (see tests/fixtures/fixtures.ts).
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
