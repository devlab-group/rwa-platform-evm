// Runs in mocked mode (default) and live mode (BASE_URL set). It assumes no
// seeded state and checks that every screen renders its heading. The
// state-dependent specs are mocked-mode only (mockOnly() in
// tests/fixtures/fixtures.ts), so this is the spec meant to exercise the full
// server stack in live mode.
import { expect, requireAdminSession, test } from "../fixtures/fixtures";

const ADMIN_ROUTES = [
  ["/admin/setup", "Setup"],
  ["/admin/assets", "Assets"],
  ["/admin/compliance", "Compliance"],
  ["/admin/inventory-sales", "Inventory & Sales"],
  ["/admin/redemptions", "Redemptions"],
  ["/admin/transactions", "Transactions"],
  ["/admin/security", "Security"],
] as const;

test.describe("Smoke - every screen renders", () => {
  for (const [path, heading] of ADMIN_ROUTES) {
    test(`admin ${heading} renders`, async ({ page }) => {
      requireAdminSession();
      await page.goto(path);
      await expect(page.getByRole("heading", { name: heading, level: 1 })).toBeVisible();
    });
  }

  test("root redirects into the admin shell", async ({ page }) => {
    await page.goto("/");
    await expect(page).toHaveURL(/\/admin\/setup$/);
  });
});
