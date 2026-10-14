import { defineConfig, devices } from "@playwright/test";

// BASE_URL selects Playwright mode:
//   - Unset: vite preview serves the production build with mocked API and wallet.
//     No backend is needed.
//   - Set: target a running server's embedded SPA without mocks. Set
//     E2E_ADMIN_PRIVATE_KEY to its admin_address key (Anvil account 0 locally)
//     for real /auth/challenge and /auth/session login before each test.
//     Without the key, tests requiring an admin session are skipped.
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
  // Start a local preview server only in mock mode; a live BASE_URL is an
  // externally managed server
  webServer: liveBaseUrl
    ? undefined
    : {
        command: `npm run preview -- --port ${PREVIEW_PORT} --strictPort`,
        url: `http://localhost:${PREVIEW_PORT}`,
        reuseExistingServer: !process.env.CI,
        timeout: 30_000,
      },
});
