// Checks the CSP meta tag that vite.config.ts's injectCsp plugin adds at build
// time, so a dropped plugin or a loosened directive fails. It runs in both
// modes because it reads shipped markup, and the live server embeds the same
// dist/index.html.
import { expect, test } from "../fixtures/fixtures";

test("ships a restrictive CSP with no unsafe-inline/unsafe-eval", async ({ page }) => {
  await page.goto("/admin/setup");

  const csp = await page
    .locator('meta[http-equiv="Content-Security-Policy"]')
    .getAttribute("content");

  expect(csp).not.toBeNull();
  expect(csp).toContain("default-src 'self'");
  expect(csp).toContain("object-src 'none'");
  expect(csp).not.toMatch(/unsafe-inline/);
  expect(csp).not.toMatch(/unsafe-eval/);
  // Browsers ignore frame-ancestors in a <meta> tag; the server sends the same
  // policy as a response header, and the directive stays here so both match
  expect(csp).toContain("frame-ancestors");
});
