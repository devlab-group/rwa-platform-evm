// Playwright fixture installs mock API and wallet unless BASE_URL selects
// a live deployment. Live mode skips mockOnly specs and uses the real backend.
// No session is seeded: tests drive the wallet challenge flow to obtain
// X-Wallet-Session when needed.
import { test as base, expect } from "@playwright/test";
import { defaultState, installMockApi, type MockState } from "./mock-api";
import { installMockWallet } from "./mock-wallet";

export const isLiveRun = Boolean(process.env.BASE_URL);

interface Fixtures {
  /** Mutable mock backing store. In live mode it is an unused placeholder. */
  api: MockState;
}

export const test = base.extend<Fixtures>({
  // `auto: true`: Playwright fixtures are lazy and run only when a test
  // destructures them, so specs that need only `page` would skip the mock
  // installation and hit the real network otherwise
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
 * Call at the top of a `describe` block (or a test) whose assertions depend on
 * seeding the mock's in-memory store. Those can't run against a live server's
 * state, so they are skipped in live mode.
 */
export function mockOnly(): void {
  test.skip(isLiveRun, "depends on seeded mock state, mocked mode only");
}

export { expect };
