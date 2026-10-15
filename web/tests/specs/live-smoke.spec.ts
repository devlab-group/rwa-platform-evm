// Heading smoke checks run with mocks by default or against BASE_URL without seeded
// state. Stateful specs use mockOnly() (tests/fixtures/fixtures.ts); this spec
// exercises every screen in the live server stack.
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
