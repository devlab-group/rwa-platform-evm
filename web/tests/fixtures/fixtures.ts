// Playwright `test` with an `api` fixture. In the default run it installs the
// mocked backend and wallet (mock-api.ts, mock-wallet.ts). When BASE_URL
// points at a live server it installs nothing, so specs marked mockOnly() are
// skipped and the rest hit the real backend.
import { test as base, expect } from "@playwright/test";
import { privateKeyToAccount } from "viem/accounts";
import { defaultState, installMockApi, type MockState } from "./mock-api";
import { installMockWallet, MOCK_WALLET_ADDRESS } from "./mock-wallet";

export const isLiveRun = Boolean(process.env.BASE_URL);

// Live mode signs in as the server's admin with this key (the one for the
// server's admin_address); it is read here only and never logged
const adminKey = process.env.E2E_ADMIN_PRIVATE_KEY as `0x${string}` | undefined;

/**
 * Call from a spec or test that needs an admin session. In live mode without
 * E2E_ADMIN_PRIVATE_KEY there is none, so the test is skipped. Mock mode is
 * unaffected.
 */
export function requireAdminSession(): void {
  test.skip(
    isLiveRun && !adminKey,
    "set E2E_ADMIN_PRIVATE_KEY to the server's admin_address key",
  );
}

/**
 * Seed a live admin JWT in mocked runs through window.__RWA_E2E_SESSION__
 * before app code loads, avoiding login on each page-object navigation.
 * lib/authSession.ts reads it at module load. The auth-session spec opts out
 * with test.use({ skipAuthBootstrap: true }) to exercise wallet login.
 */
export const MOCK_ADMIN_SESSION = {
  token: "mock-e2e-session-token",
  expiresAt: Math.floor(Date.now() / 1000) + 3600,
  role: "admin",
  address: MOCK_WALLET_ADDRESS,
};

interface Fixtures {
  /** Mutable mock backing store; in live mode it is an unused placeholder */
  api: MockState;
  /** Set with `test.use({ skipAuthBootstrap: true })` to test the real login form instead of a seeded session */
  skipAuthBootstrap: boolean;
}

export const test = base.extend<Fixtures>({
  skipAuthBootstrap: [false, { option: true }],

  // `auto: true` runs the fixture for every test. Playwright fixtures are lazy
  // and otherwise run only when a test destructures them, so a spec that uses
  // only `page` would skip the mocks and hit the real network.
  api: [
    async ({ page, skipAuthBootstrap }, use) => {
      if (isLiveRun) {
        if (adminKey && !skipAuthBootstrap) {
          // Real login: the SPA is served by the same server, so BASE_URL is the API base
          const account = privateKeyToAccount(adminKey);
          const challenge = await page.request.post("/auth/challenge", {
            data: { address: account.address },
          });
          if (!challenge.ok())
            throw new Error(
              `POST /auth/challenge failed: ${challenge.status()}`,
            );
          const { message } = (await challenge.json()) as { message: string };
          const signature = await account.signMessage({ message });
          const res = await page.request.post("/auth/session", {
            data: { address: account.address, signature },
          });
          if (!res.ok())
            throw new Error(`POST /auth/session failed: ${res.status()}`);
          const session = await res.json();
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
 * Call at the top of a `describe` block or test that depends on seeding the
 * mock's in-memory store. Such tests cannot run against a live server's state,
 * so live mode skips them.
 */
export function mockOnly(): void {
  test.skip(
    isLiveRun,
    "depends on seeded mock state, so it runs in mock mode only",
  );
}

export { expect };
