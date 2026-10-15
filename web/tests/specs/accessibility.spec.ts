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
];

// Every test here scans an admin screen
test.beforeEach(() => requireAdminSession());

for (const path of ROUTES) {
  test(`${path} has no serious/critical axe violations`, async ({ page }) => {
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
