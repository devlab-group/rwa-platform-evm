import { afterEach, describe, expect, it, vi } from "vitest";
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

// Polls until the background IndexedDB write or delete has landed
const storedEventually = (expected: AuthSession | undefined) =>
  vi.waitFor(async () =>
    expect(await idbGet<AuthSession>(STORAGE_KEY)).toEqual(expected),
  );

describe("authSession (wallet-JWT)", () => {
  afterEach(async () => {
    clearSession();
    await storedEventually(undefined);
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

  it("treats an already-expired session as absent and drops it from IndexedDB", async () => {
    const stale = makeSession({
      token: "stale",
      expiresAt: Math.floor(Date.now() / 1000) - 1,
    });
    setSession(stale);
    // The background write lands first, so the delete below is observable
    await storedEventually(stale);

    expect(getSession()).toBeUndefined();
    await storedEventually(undefined);
    // Once seen as expired, the session stays cleared
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
    await storedEventually(session);

    clearSession();
    await storedEventually(undefined);
  });

  it("hydrates a persisted, unexpired session from IndexedDB into the mirror", async () => {
    // Seed IndexedDB directly, then hydrate to simulate a fresh page without the in-memory session
    const session = makeSession({ token: "rehydrated" });
    await idbSet(STORAGE_KEY, session);
    expect(getSession()).toBeUndefined();

    await hydrateSession();
    expect(getSession()?.token).toBe("rehydrated");
  });

  it("stays signed out when IndexedDB cannot be read, and still notifies", async () => {
    const original = globalThis.indexedDB;
    // @ts-expect-error simulate a browser with storage disabled
    globalThis.indexedDB = undefined;
    const listener = vi.fn();
    const unsubscribe = subscribeSession(listener);
    try {
      await expect(hydrateSession()).resolves.toBeUndefined();
    } finally {
      unsubscribe();
      globalThis.indexedDB = original;
    }
    expect(getSession()).toBeUndefined();
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("does not hydrate an expired persisted session (and drops it)", async () => {
    const expired = makeSession({
      token: "old",
      expiresAt: Math.floor(Date.now() / 1000) - 5,
    });
    await idbSet(STORAGE_KEY, expired);

    await hydrateSession();
    expect(getSession()).toBeUndefined();
    // hydrateSession deletes expired storage entries in the background
    await storedEventually(undefined);
  });
});
