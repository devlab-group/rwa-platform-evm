// axe-core scans of every admin screen for missing labels and roles, contrast
// problems, and landmark and heading structure issues. The scans check markup,
// not seeded state, so mockOnly() does not apply.
import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";
import { expect, requireAdminSession, test } from "../fixtures/fixtures";

// Route and its h1; the scan waits for the heading and for every section to finish loading
const ROUTES: ReadonlyArray<readonly [string, string]> = [
  ["/admin/setup", "Setup"],
  ["/admin/assets", "Assets"],
  ["/admin/compliance", "Compliance"],
  ["/admin/inventory-sales", "Inventory & Sales"],
  ["/admin/redemptions", "Redemptions"],
  ["/admin/transactions", "Transactions"],
  ["/admin/security", "Security"],
  ["/admin/income", "Income"],
];

// Wait for the heading and for loading placeholders to disappear
async function expectScreenLoaded(page: Page, heading: string) {
  await expect(page.getByRole("heading", { name: heading, level: 1 })).toBeVisible();
  await expect(page.getByText("Loading…")).toHaveCount(0);
}

// Asserts zero WCAG A/AA violations of any impact
async function expectNoAxeViolations(page: Page) {
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  expect(
    results.violations,
    results.violations
      .map((v) => `${v.id} [${v.impact}]: ${v.help} (${v.nodes.length} node(s))`)
      .join("\n"),
  ).toEqual([]);
}

// Every test here scans an admin screen
test.beforeEach(() => requireAdminSession());

for (const [path, heading] of ROUTES) {
  test(`${path} has no axe violations`, async ({ page, api }) => {
    // Income and the v3 Security sections render only for an rwa-v3 project
    if (path === "/admin/income") {
      api.project.version = "rwa-v3";
      api.project.addresses = { ...api.project.addresses!, incomeDistributor: `0x${"9a".repeat(20)}` };
      api.distributions.push({
        preparationId: "prep-a11y",
        status: "Prepared",
        merkleRoot: `0x${"ab".repeat(32)}`,
        total: "5000000",
        recordBlock: 90,
        claimDeadline: Math.floor(Date.now() / 1000) + 9 * 86_400,
        snapshotURI: "ipfs://x",
        snapshotSha256: `0x${"cd".repeat(32)}`,
        eligibleSupply: "1",
        distributed: "5000000",
        holderCount: 2,
        createdAt: new Date().toISOString(),
      });
    }
    await page.goto(path);
    await expectScreenLoaded(page, heading);

    await expectNoAxeViolations(page);
  });
}

test("/admin/security with an rwa-v3 oracle project has no axe violations", async ({ page, api }) => {
  api.project.version = "rwa-v3";
  api.project.strategyKind = "Oracle";
  api.project.minPurchaseQuote = "25000000";
  api.project.oracle = {
    feed: `0x${"fe".repeat(20)}`,
    maxAge: 3600,
    purchaseSpreadBps: 50,
    redemptionSpreadBps: 75,
    feedDecimals: 8,
    available: true,
    price: "250000000",
    updatedAt: Math.floor(Date.now() / 1000),
  };
  await page.goto("/admin/security");
  await expectScreenLoaded(page, "Security");
  // The oracle section renders only for an Oracle strategy
  await expect(page.getByText(/purchase spread/i).first()).toBeVisible();
  await expectNoAxeViolations(page);
});

test.describe("signed-out", () => {
  test.use({ skipAuthBootstrap: true });

  test("the admin sign-in screen has no axe violations", async ({ page }) => {
    await page.goto("/admin/setup");
    await expect(page.getByRole("heading", { name: "Admin sign-in" })).toBeVisible();
    await expectNoAxeViolations(page);
  });
});

test("skip link moves focus to main content", async ({ page }) => {
  await page.goto("/admin/setup");
  // The first Tab from the top of the page must focus the skip link
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Skip to main content" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator("#main-content")).toBeFocused();
});

test("client-side navigation moves focus to the new page's main content", async ({ page }) => {
  await page.goto("/admin/setup");
  await expect(page.locator("#main-content")).not.toBeFocused();

  await page.getByRole("link", { name: "Assets" }).click();
  await expect(page).toHaveURL(/\/admin\/assets$/);
  // A client-side route change does not reload the page, so the app moves
  // focus explicitly to signal the change to screen reader users
  await expect(page.locator("#main-content")).toBeFocused();
});
