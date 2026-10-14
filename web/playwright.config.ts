import { defineConfig, devices } from "@playwright/test";

// BASE_URL selects the run mode:
//   - Unset: vite preview serves the production build with API and wallet mocks;
//     no backend is needed.
//   - Set: use a running server without mocks. Global setup logs in once with
//     E2E_ADMIN_PRIVATE_KEY, the key for security.admin_address (Anvil account 0
//     locally). Admin-dependent tests skip when the key is unset.
const liveBaseUrl = process.env.BASE_URL;
const PREVIEW_PORT = 4173;

export default defineConfig({
  testDir: "./tests",
  globalSetup: "./tests/global-setup.ts",
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
