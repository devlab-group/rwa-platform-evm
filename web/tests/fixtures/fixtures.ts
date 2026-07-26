// Custom Playwright `test` with a `api` fixture that installs the mocked
// backend + mocked wallet (see mock-api.ts / mock-wallet.ts) for the default
// run, and steps aside entirely when BASE_URL points at a live server so
// specs marked mockOnly() are skipped and the rest hit the real backend.
import { test as base, expect } from "@playwright/test";
import { defaultState, installMockApi, type MockState } from "./mock-api";
import { installMockWallet, MOCK_WALLET_ADDRESS } from "./mock-wallet";

export const isLiveRun = Boolean(process.env.BASE_URL);

/**
 * Admin routes require a live admin session — now a wallet-signature-minted
 * JWT (see src/lib/authSession.ts, src/components/Login.tsx) instead of the
 * old operator API-key exchange. Playwright page objects do a full
 * `page.goto(path)` per screen, and although the JWT is persisted in
 * IndexedDB, seeding it directly is simplest: the `api` fixture below sets
 * `window.__RWA_E2E_SESSION__` (read once at module load by lib/authSession.ts)
 * before any app code runs, so page objects' `goto()` needn't drive the login
 * form on every navigation. tests/specs/auth-session.spec.ts opts out via
 * `test.use({ skipAuthBootstrap: true })` to exercise the real
 * connect → sign → verify flow end-to-end instead.
 */
export const MOCK_ADMIN_SESSION = {
  token: "mock-e2e-session-token",
  expiresAt: Math.floor(Date.now() / 1000) + 3600,
  role: "admin",
  address: MOCK_WALLET_ADDRESS,
};

interface Fixtures {
  /** Mutable mock backing store. In live mode this is an unused placeholder — nothing reads it. */
  api: MockState;
  /** Set via `test.use({ skipAuthBootstrap: true })` to test the real login form instead of a pre-seeded session. */
  skipAuthBootstrap: boolean;
}

export const test = base.extend<Fixtures>({
  skipAuthBootstrap: [false, { option: true }],

  // `auto: true` — Playwright fixtures are lazy and only run when a test
  // destructures them. Specs that only need `page` (no seeded state) would
  // otherwise silently skip mock installation and hit the real network.
  api: [
    async ({ page, skipAuthBootstrap }, use) => {
      if (isLiveRun) {
        await use(defaultState());
        return;
      }
      const state = await installMockApi(page);
      await installMockWallet(page);
      if (!skipAuthBootstrap) {
        await page.addInitScript((session) => {
          (window as unknown as { __RWA_E2E_SESSION__?: unknown }).__RWA_E2E_SESSION__ = session;
        }, MOCK_ADMIN_SESSION);
      }
      await use(state);
    },
    { auto: true },
  ],
});

/**
 * Call at the top of a `describe` block (or a test) whose assertions depend
 * on seeding the mock's in-memory store — those can't run meaningfully
 * against a live server's real state, so they're skipped in live mode
 * rather than silently mutating a fixture nobody reads.
 */
export function mockOnly(): void {
  test.skip(isLiveRun, "depends on seeded mock state — mocked-mode only");
}

export { expect };
