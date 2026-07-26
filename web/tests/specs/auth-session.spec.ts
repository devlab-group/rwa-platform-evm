// Admin auth is now wallet-signature based: the connected wallet signs a
// challenge from POST /auth/challenge and the signature is exchanged at
// POST /auth/session for an admin JWT persisted in IndexedDB (see
// src/lib/authSession.ts, src/components/Login.tsx). Every other admin spec
// relies on the `api` fixture's default session bootstrap (see
// tests/fixtures/fixtures.ts) so page objects' goto() needn't drive the login
// form; this spec opts out (`skipAuthBootstrap`) to exercise the real
// connect → sign → verify, logout, and 401-forces-relogin flows end to end.
import { expect, test } from "../fixtures/fixtures";

/**
 * Drives the real login: connect the wallet (the Login screen's own connect
 * button, shown while disconnected), then sign the challenge. Connection is
 * never silent — the admin must connect first.
 */
async function connectAndSignIn(page: import("@playwright/test").Page) {
  await page.getByRole("button", { name: "Connect wallet to sign in" }).click();
  await page.getByRole("button", { name: "Sign in with wallet" }).click();
}

/** Reads the persisted admin JWT out of IndexedDB (lib/idb.ts: db "rwa", store "kv"). */
async function readPersistedToken(page: import("@playwright/test").Page) {
  return page.evaluate(
    () =>
      new Promise<string | null>((resolve) => {
        const open = indexedDB.open("rwa", 1);
        open.onsuccess = () => {
          const db = open.result;
          if (!db.objectStoreNames.contains("kv")) return resolve(null);
          const req = db.transaction("kv").objectStore("kv").get("authSession");
          req.onsuccess = () =>
            resolve((req.result as { token?: string } | undefined)?.token ?? null);
          req.onerror = () => resolve(null);
        };
        open.onerror = () => resolve(null);
      }),
  );
}

test.describe("Admin wallet-signature session", () => {
  test.use({ skipAuthBootstrap: true });

  test("an unauthenticated admin route shows the login form, not the screen", async ({ page }) => {
    await page.goto("/admin/setup");

    await expect(page.getByRole("heading", { name: "Admin sign-in" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Setup", exact: true })).toHaveCount(0);
  });

  test("signing in exchanges a wallet signature for a JWT persisted in IndexedDB, not web storage", async ({
    page,
  }) => {
    await page.goto("/admin/setup");

    const requestPromise = page.waitForRequest(
      (r) => r.url().endsWith("/auth/session") && r.method() === "POST",
    );
    // Connect the wallet, then sign the challenge → personal_sign → session.
    await connectAndSignIn(page);
    const request = await requestPromise;
    const body = request.postDataJSON() as { address: string; signature: string };
    expect(body.address).toBeTruthy();
    expect(body.signature).toBeTruthy();

    await expect(page.getByRole("heading", { name: "Setup", exact: true })).toBeVisible();
    await expect(page.getByText(/Signed in as/)).toBeVisible();

    // The JWT is deliberately persisted in IndexedDB (the user accepted browser
    // storage for it) — but never in localStorage or sessionStorage.
    expect(await readPersistedToken(page)).toContain("mock-jwt");
    const storageDump = await page.evaluate(() => ({
      local: { ...window.localStorage },
      session: { ...window.sessionStorage },
    }));
    expect(JSON.stringify(storageDump).toLowerCase()).not.toContain("mock-jwt");
    expect(Object.keys(storageDump.local)).toHaveLength(0);
    expect(Object.keys(storageDump.session)).toHaveLength(0);
  });

  test("the persisted JWT survives a full page reload (IndexedDB rehydration)", async ({ page }) => {
    await page.goto("/admin/setup");
    await connectAndSignIn(page);
    await expect(page.getByRole("heading", { name: "Setup", exact: true })).toBeVisible();

    // A hard reload loses the in-memory mirror; hydrateSession() must restore
    // the token from IndexedDB so the admin stays signed in without re-signing.
    await page.reload();
    await expect(page.getByRole("heading", { name: "Setup", exact: true })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Admin sign-in" })).toHaveCount(0);
  });

  test("logging out clears the session and returns to the login form", async ({ page }) => {
    await page.goto("/admin/setup");
    await connectAndSignIn(page);
    await expect(page.getByRole("heading", { name: "Setup", exact: true })).toBeVisible();

    await page.getByRole("button", { name: "Log out" }).click();

    await expect(page.getByRole("heading", { name: "Admin sign-in" })).toBeVisible();
    // Logout drops the persisted token too.
    expect(await readPersistedToken(page)).toBeNull();
  });

  test("a 401 from the API clears the session and forces the login form again", async ({ page }) => {
    await page.goto("/admin/setup");
    await connectAndSignIn(page);
    await expect(page.getByRole("heading", { name: "Setup", exact: true })).toBeVisible();

    // Simulate an expired/revoked JWT on the next admin API read.
    await page.route("**/api/v1/project", (route) =>
      route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ code: "unauthorized", message: "session expired" }),
      }),
    );

    // Client-side navigation (NavLink) — no full reload — so this exercises the
    // 401 handler itself rather than re-triggering the login gate via reload.
    await page.getByRole("link", { name: "Security" }).click();

    await expect(page.getByRole("heading", { name: "Admin sign-in" })).toBeVisible();
  });

  test("a wallet that isn't the admin is rejected with an inline error and no session", async ({ page }) => {
    await page.goto("/admin/setup");
    // Simulate "not the project admin" by making the exchange itself 401.
    await page.route("**/auth/session", (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      return route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ code: "unauthorized", message: "not admin" }),
      });
    });

    await connectAndSignIn(page);

    await expect(page.getByRole("alert")).toContainText(/not the project admin/i);
    await expect(page.getByRole("heading", { name: "Admin sign-in" })).toBeVisible();
    expect(await readPersistedToken(page)).toBeNull();
  });
});
