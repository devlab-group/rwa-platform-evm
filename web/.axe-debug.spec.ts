import AxeBuilder from "@axe-core/playwright";
import { test } from "./tests/fixtures/fixtures";

const ROUTES = [
  "/admin/setup", "/admin/assets", "/admin/compliance", "/admin/inventory-sales",
  "/admin/redemptions", "/admin/transactions", "/admin/security", "/investor",
];

for (const path of ROUTES) {
  test(`debug ${path}`, async ({ page }) => {
    await page.goto(path);
    await page.waitForLoadState("networkidle");
    const results = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    console.log(`\n=== ${path} (${results.violations.length}) ===`);
    for (const v of results.violations) {
      console.log(`[${v.impact}] ${v.id}: ${v.help} — ${v.nodes.length} node(s)`);
      for (const n of v.nodes.slice(0, 4)) {
        console.log("   ", n.target.join(" "), "|", n.failureSummary?.split("\n")[0]);
      }
    }
  });
}
