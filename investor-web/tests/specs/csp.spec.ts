// Regression check for the CSP meta tag injected at build time (see the
// injectCsp plugin in vite.config.ts); it fails if the plugin is dropped or a
// directive is loosened. It runs in both modes because it checks the built
// markup, not seeded state.
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
  // Browsers ignore frame-ancestors in a <meta> tag, so it protects nothing
  // here. It stays in the policy so the serving host can copy it into a real
  // response header
  expect(csp).toContain("frame-ancestors");
});
