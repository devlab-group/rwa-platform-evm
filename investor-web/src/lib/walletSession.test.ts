import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  clearWalletSession,
  getWalletSession,
  setWalletSession,
} from "./walletSession";

const ADDRESS = "0x00000000000000000000000000000000000000Aa";
const SESSION = {
  token: "t",
  expiresAt: new Date(Date.now() + 60_000).toISOString(),
};

describe("walletSession", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("round-trips a session per address", () => {
    setWalletSession(ADDRESS, SESSION);
    expect(getWalletSession(ADDRESS.toLowerCase())).toEqual(SESSION);
    clearWalletSession(ADDRESS);
    expect(getWalletSession(ADDRESS)).toBeUndefined();
  });

  describe("expiry", () => {
    const NOW = new Date("2026-03-29T12:00:00.000Z");
    const KEY = `rwa.walletSession.${ADDRESS.toLowerCase()}`;

    beforeEach(() => {
      vi.useFakeTimers();
      vi.setSystemTime(NOW);
    });
    afterEach(() => vi.useRealTimers());

    it("returns a session that expires in the future", () => {
      const live = {
        token: "t",
        expiresAt: new Date(NOW.getTime() + 1).toISOString(),
      };
      setWalletSession(ADDRESS, live);
      expect(getWalletSession(ADDRESS)).toEqual(live);
    });

    it("drops and removes a session whose expiry has passed", () => {
      setWalletSession(ADDRESS, {
        token: "t",
        expiresAt: new Date(NOW.getTime() - 1).toISOString(),
      });
      expect(getWalletSession(ADDRESS)).toBeUndefined();
      expect(window.localStorage.getItem(KEY)).toBeNull();
    });

    it("treats a session expiring exactly now as expired and removes it", () => {
      setWalletSession(ADDRESS, { token: "t", expiresAt: NOW.toISOString() });
      expect(getWalletSession(ADDRESS)).toBeUndefined();
      expect(window.localStorage.getItem(KEY)).toBeNull();
    });

    it("expires a stored session as the clock advances past its expiry", () => {
      setWalletSession(ADDRESS, {
        token: "t",
        expiresAt: new Date(NOW.getTime() + 60_000).toISOString(),
      });
      expect(getWalletSession(ADDRESS)?.token).toBe("t");
      vi.setSystemTime(new Date(NOW.getTime() + 60_000));
      expect(getWalletSession(ADDRESS)).toBeUndefined();
    });
  });

  it("treats blocked storage as no session instead of throwing", () => {
    vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });
    expect(() => setWalletSession(ADDRESS, SESSION)).not.toThrow();
    expect(getWalletSession(ADDRESS)).toBeUndefined();
    expect(() => clearWalletSession(ADDRESS)).not.toThrow();
  });

  it("ignores a full storage quota", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("full", "QuotaExceededError");
    });
    expect(() => setWalletSession(ADDRESS, SESSION)).not.toThrow();
    expect(getWalletSession(ADDRESS)).toBeUndefined();
  });
});
