// axe-core scans of every admin screen for missing labels and roles, contrast
// problems, and landmark and heading structure issues. The scans check markup,
// not seeded state, so mockOnly() does not apply.
import AxeBuilder from "@axe-core/playwright";
import { expect, requireAdminSession, test } from "../fixtures/fixtures";

const ROUTES = [
  "/admin/setup",
  "/admin/assets",
  "/admin/compliance",
  "/admin/inventory-sales",
  "/admin/redemptions",
  "/admin/transactions",
  "/admin/security",
  "/admin/income",
];

// Every test here scans an admin screen
test.beforeEach(() => requireAdminSession());

for (const path of ROUTES) {
  test(`${path} has no serious/critical axe violations`, async ({ page, api }) => {
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
    // Wait for the lazy-loaded route chunk to mount before scanning
    await page.waitForLoadState("networkidle");

    const results = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
      .analyze();

    const seriousOrWorse = results.violations.filter(
      (v) => v.impact === "serious" || v.impact === "critical",
    );
    expect(
      seriousOrWorse,
      seriousOrWorse.map((v) => `${v.id}: ${v.help} (${v.nodes.length} node(s))`).join("\n"),
    ).toEqual([]);
  });
}

test("/admin/security with an rwa-v3 oracle project has no serious/critical axe violations", async ({ page, api }) => {
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
  await page.waitForLoadState("networkidle");
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  const seriousOrWorse = results.violations.filter(
    (v) => v.impact === "serious" || v.impact === "critical",
  );
  expect(
    seriousOrWorse,
    seriousOrWorse.map((v) => `${v.id}: ${v.help} (${v.nodes.length} node(s))`).join("\n"),
  ).toEqual([]);
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
