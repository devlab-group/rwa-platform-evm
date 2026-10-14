import { afterEach, describe, expect, it, vi } from "vitest";
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
