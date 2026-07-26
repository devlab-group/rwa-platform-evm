// Regression check for the CSP meta tag injected at build time (see
// vite.config.ts's injectCsp plugin) — catches someone accidentally
// dropping the plugin or loosening a directive. Runs in both modes: it's
// checking shipped markup, not seeded state, and the live server embeds the
// same dist/index.html.
import { expect, test } from "../fixtures/fixtures";

test("ships a restrictive CSP with no unsafe-inline/unsafe-eval", async ({ page }) => {
  await page.goto("/");

  const csp = await page
    .locator('meta[http-equiv="Content-Security-Policy"]')
    .getAttribute("content");

  expect(csp).not.toBeNull();
  expect(csp).toContain("default-src 'self'");
  expect(csp).toContain("object-src 'none'");
  expect(csp).not.toMatch(/unsafe-inline/);
  expect(csp).not.toMatch(/unsafe-eval/);
  // frame-ancestors is documented here for the server to also send as a real
  // header — browsers ignore it in a <meta> tag, so its presence here isn't
  // itself protective, but its absence would mean nobody thought about it.
  expect(csp).toContain("frame-ancestors");
});
