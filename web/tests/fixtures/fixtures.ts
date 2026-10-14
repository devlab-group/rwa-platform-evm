// Playwright api fixture: install API and wallet mocks by default. With BASE_URL,
// use the live backend and skip mockOnly() specs.
import { test as base, expect } from "@playwright/test";
import { defaultState, installMockApi, type MockState } from "./mock-api";
import { installMockWallet, MOCK_WALLET_ADDRESS } from "./mock-wallet";

export const isLiveRun = Boolean(process.env.BASE_URL);

// Global setup supplies E2E_ADMIN_SESSION. This fixture checks whether
// E2E_ADMIN_PRIVATE_KEY is set to gate admin tests; it does not log the key.
const adminKey = process.env.E2E_ADMIN_PRIVATE_KEY;
const liveAdminSession = process.env.E2E_ADMIN_SESSION;

/**
 * Skip admin-dependent tests in live mode when E2E_ADMIN_PRIVATE_KEY is unset.
 * Mock runs are unaffected.
 */
export function requireAdminSession(): void {
  test.skip(
    isLiveRun && !adminKey,
    "set E2E_ADMIN_PRIVATE_KEY to the server's admin_address key",
  );
}

/**
 * Mock session injected through window.__RWA_E2E_SESSION__ before app scripts,
 * avoiding login on page reloads. lib/authSession.ts reads it at module load.
 * The auth-session spec disables seeding with skipAuthBootstrap to test login.
 */
export const MOCK_ADMIN_SESSION = {
  token: "mock-e2e-session-token",
  expiresAt: Math.floor(Date.now() / 1000) + 3600,
  role: "admin",
  address: MOCK_WALLET_ADDRESS,
};

interface Fixtures {
  // Mutable mock store; unused placeholder in live mode
  api: MockState;
  // Disable session seeding with test.use({ skipAuthBootstrap: true }) to test login
  skipAuthBootstrap: boolean;
}

export const test = base.extend<Fixtures>({
  skipAuthBootstrap: [false, { option: true }],

  // Fixtures are lazy; auto-run this setup for tests that request only page
  api: [
    async ({ page, skipAuthBootstrap }, use) => {
      if (isLiveRun) {
        if (liveAdminSession && !skipAuthBootstrap) {
          const session: unknown = JSON.parse(liveAdminSession);
          await page.addInitScript((s) => {
            (
              window as unknown as { __RWA_E2E_SESSION__?: unknown }
            ).__RWA_E2E_SESSION__ = s;
          }, session);
        }
        await use(defaultState());
        return;
      }
      const state = await installMockApi(page);
      await installMockWallet(page);
      if (!skipAuthBootstrap) {
        await page.addInitScript((session) => {
          (
            window as unknown as { __RWA_E2E_SESSION__?: unknown }
          ).__RWA_E2E_SESSION__ = session;
        }, MOCK_ADMIN_SESSION);
      }
      await use(state);
    },
    { auto: true },
  ],
});

/**
 * Skip tests that depend on seeded mock state in live mode.
 * Call at the start of a describe block or test.
 */
export function mockOnly(): void {
  test.skip(
    isLiveRun,
    "depends on seeded mock state, so it runs in mock mode only",
  );
}

export { expect };
