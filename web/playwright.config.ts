import { defineConfig, devices } from "@playwright/test";

// Two modes, selected by whether BASE_URL is set:
//  - default (no BASE_URL): serves the production build via `vite preview` and
//    every spec installs a mocked API + mocked wallet (see tests/fixtures) —
//    deterministic, no backend required, this is what CI runs.
//  - live (BASE_URL=http://host:port): points straight at a running
//    server+SPA (the Go binary embeds the built SPA) for the final
//    full-stack exit-gate run against real Anvil/Mongo/IPFS. Specs skip
//    installing mocks in this mode (see tests/fixtures/fixtures.ts).
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
  // Only spin up a local static server in mocked mode; a live BASE_URL means
  // the target server is managed externally (already running against Anvil).
  webServer: liveBaseUrl
    ? undefined
    : {
        command: `npm run preview -- --port ${PREVIEW_PORT} --strictPort`,
        url: `http://localhost:${PREVIEW_PORT}`,
        reuseExistingServer: !process.env.CI,
        timeout: 30_000,
      },
});
