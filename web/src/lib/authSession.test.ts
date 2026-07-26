import { afterEach, describe, expect, it } from "vitest";
import {
  clearSession,
  getSession,
  hydrateSession,
  setSession,
  subscribeSession,
  type AuthSession,
} from "./authSession";
import { idbGet, idbSet } from "./idb";

const STORAGE_KEY = "authSession";

function makeSession(overrides: Partial<AuthSession> = {}): AuthSession {
  return {
    token: "jwt-token",
    expiresAt: Math.floor(Date.now() / 1000) + 3600,
    role: "admin",
    address: "0x1111111111111111111111111111111111111111",
    ...overrides,
  };
}

/** Lets the fire-and-forget IndexedDB write-through settle. */
const flush = () => new Promise((r) => setTimeout(r, 0));

describe("authSession (wallet-JWT)", () => {
  afterEach(async () => {
    clearSession();
    await flush();
  });

  it("starts signed out", () => {
    expect(getSession()).toBeUndefined();
  });

  it("returns exactly what was set", () => {
    const session = makeSession();
    setSession(session);
    expect(getSession()).toEqual(session);
  });

  it("clears via setSession(undefined) and via clearSession()", () => {
    setSession(makeSession({ token: "t1" }));
    setSession(undefined);
    expect(getSession()).toBeUndefined();

    setSession(makeSession({ token: "t2" }));
    clearSession();
    expect(getSession()).toBeUndefined();
  });

  it("treats an already-expired session as absent and drops it", () => {
    setSession(
      makeSession({
        token: "stale",
        expiresAt: Math.floor(Date.now() / 1000) - 1,
      }),
    );
    expect(getSession()).toBeUndefined();
    // Once observed as expired, it stays cleared (not just hidden this call).
    expect(getSession()).toBeUndefined();
  });

  it("notifies subscribers on every change, including clears", () => {
    const calls: Array<ReturnType<typeof getSession>> = [];
    const unsubscribe = subscribeSession(() => calls.push(getSession()));

    setSession(makeSession({ token: "t1" }));
    setSession(undefined);
    unsubscribe();
    setSession(makeSession({ token: "t2" }));

    expect(calls).toHaveLength(2);
    expect(calls[0]?.token).toBe("t1");
    expect(calls[1]).toBeUndefined();
  });

  it("never touches localStorage or sessionStorage", () => {
    setSession(makeSession({ token: "secret-token" }));
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);
  });

  it("persists the JWT to IndexedDB on set and removes it on clear", async () => {
    const session = makeSession({ token: "persisted-jwt" });
    setSession(session);
    await flush();
    expect(await idbGet<AuthSession>(STORAGE_KEY)).toEqual(session);

    clearSession();
    await flush();
    expect(await idbGet<AuthSession>(STORAGE_KEY)).toBeUndefined();
  });

  it("hydrates a persisted, unexpired session from IndexedDB into the mirror", async () => {
    // Model a reload: write the token straight to IndexedDB (bypassing the
    // in-memory mirror, which a fresh page load starts empty), then hydrate.
    const session = makeSession({ token: "rehydrated" });
    await idbSet(STORAGE_KEY, session);
    expect(getSession()).toBeUndefined();

    await hydrateSession();
    expect(getSession()?.token).toBe("rehydrated");
  });

  it("does not hydrate an expired persisted session (and drops it)", async () => {
    const expired = makeSession({
      token: "old",
      expiresAt: Math.floor(Date.now() / 1000) - 5,
    });
    await idbSet(STORAGE_KEY, expired);

    await hydrateSession();
    expect(getSession()).toBeUndefined();
    await flush(); // the prune is fire-and-forget inside hydrateSession
    // The stale token was pruned from storage, not just ignored.
    expect(await idbGet<AuthSession>(STORAGE_KEY)).toBeUndefined();
  });
});
