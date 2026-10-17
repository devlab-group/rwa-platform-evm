import { describe, expect, it } from "vitest";
import { idbDelete, idbGet, idbSet } from "./idb";

describe("idb key-value store", () => {
  it("returns undefined for a key that was never set", async () => {
    await expect(idbGet("missing")).resolves.toBeUndefined();
  });

  it("round-trips a structured value", async () => {
    const value = { token: "t", expiresAt: 1, nested: { a: [1, 2] } };
    await idbSet("k", value);
    await expect(idbGet("k")).resolves.toEqual(value);
  });

  it("overwrites an existing key", async () => {
    await idbSet("k", "first");
    await idbSet("k", "second");
    await expect(idbGet("k")).resolves.toBe("second");
  });

  it("keeps keys independent and deletes only the named one", async () => {
    await idbSet("a", 1);
    await idbSet("b", 2);
    await idbDelete("a");
    await expect(idbGet("a")).resolves.toBeUndefined();
    await expect(idbGet("b")).resolves.toBe(2);
  });

  it("deleting a missing key resolves", async () => {
    await expect(idbDelete("never-set")).resolves.toBeUndefined();
  });

  it("rejects with a clear error when IndexedDB is unavailable", async () => {
    const original = globalThis.indexedDB;
    // @ts-expect-error simulate a browser with storage disabled
    globalThis.indexedDB = undefined;
    try {
      await expect(idbGet("k")).rejects.toThrow(/not available/i);
    } finally {
      globalThis.indexedDB = original;
    }
  });
});
