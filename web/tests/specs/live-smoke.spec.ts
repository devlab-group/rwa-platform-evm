// Runs in both mocked mode (default) and live mode (BASE_URL set) — no
// seeded state assumptions, just "does every screen render its chrome
// without crashing." The state-dependent specs alongside this file are
// mocked-mode only (see mockOnly() in tests/fixtures/fixtures.ts); this is
// the one file that actually exercises the real Anvil/Mongo/IPFS stack
// end-to-end.
import { expect, test } from "../fixtures/fixtures";

const ADMIN_ROUTES = [
  ["/admin/setup", "Setup"],
  ["/admin/assets", "Assets"],
  ["/admin/compliance", "Compliance"],
  ["/admin/inventory-sales", "Inventory & Sales"],
  ["/admin/redemptions", "Redemptions"],
  ["/admin/transactions", "Transactions"],
  ["/admin/security", "Security"],
] as const;

test.describe("Smoke — every screen renders", () => {
  for (const [path, heading] of ADMIN_ROUTES) {
    test(`admin ${heading} renders`, async ({ page }) => {
      await page.goto(path);
      await expect(page.getByRole("heading", { name: heading, level: 1 })).toBeVisible();
    });
  }

  test("root redirects into the admin shell", async ({ page }) => {
    await page.goto("/");
    await expect(page).toHaveURL(/\/admin\/setup$/);
  });
});
