// Automated axe-core scan of the investor screen for WCAG 2.0 and 2.1 level A
// and AA violations such as missing labels or roles, insufficient contrast and
// landmark or heading problems. It runs in both mocked and live mode because it
// checks markup and does not depend on seeded state, so mockOnly() doesn't
// apply.
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "../fixtures/fixtures";

test("/ has no serious/critical axe violations", async ({ page }) => {
  await page.goto("/");
  // Let the lazy-loaded route chunk finish mounting before scanning.
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
  await page.goto("/");
  // Tab once from the top of the page: the skip link is the first focusable
  // element and must be focusable
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Skip to main content" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator("#main-content")).toBeFocused();
});
