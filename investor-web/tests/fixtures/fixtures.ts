// Custom Playwright `test` with an `api` fixture that installs the mocked
// backend + mocked wallet (see mock-api.ts / mock-wallet.ts) for the default
// run, and steps aside entirely when BASE_URL points at a live server so
// specs marked mockOnly() are skipped and the rest hit the real backend.
//
// There is no session to seed: this app authenticates nothing at page load.
// The only credential it ever holds is the X-Wallet-Session minted by the
// in-app challenge/sign flow, which the specs drive through the UI.
import { test as base, expect } from "@playwright/test";
import { defaultState, installMockApi, type MockState } from "./mock-api";
import { installMockWallet } from "./mock-wallet";

export const isLiveRun = Boolean(process.env.BASE_URL);

interface Fixtures {
  /** Mutable mock backing store. In live mode this is an unused placeholder — nothing reads it. */
  api: MockState;
}

export const test = base.extend<Fixtures>({
  // `auto: true` — Playwright fixtures are lazy and only run when a test
  // destructures them. Specs that only need `page` (no seeded state) would
  // otherwise silently skip mock installation and hit the real network.
  api: [
    async ({ page }, use) => {
      if (isLiveRun) {
        await use(defaultState());
        return;
      }
      const state = await installMockApi(page);
      await installMockWallet(page);
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
