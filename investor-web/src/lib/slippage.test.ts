import { describe, expect, it } from "vitest";
import {
  applySlippageCeil,
  applySlippageFloor,
  MAX_SLIPPAGE_BPS,
} from "./slippage";

describe("applySlippageCeil", () => {
  it("adds the requested tolerance, rounded up", () => {
    // 1_000_000 * 1.5% = 1_015_000 exactly
    expect(applySlippageCeil("1000000", 150)).toBe("1015000");
  });

  it("rounds a fractional result up, never down", () => {
    // Round 3 * 1.0001 = 3.0003 up to 4
    expect(applySlippageCeil("3", 1)).toBe("4");
  });

  it("caps an unreasonably large slippage value at MAX_SLIPPAGE_BPS", () => {
    // Clamp extreme slippage (1_000_000 bps = 10,000%) to limit approved spend
    const uncapped = applySlippageCeil("1000000", 1_000_000);
    const capped = applySlippageCeil("1000000", MAX_SLIPPAGE_BPS);
    expect(uncapped).toBe(capped);
    expect(uncapped).toBe("1500000"); // 1_000_000 * (1 + 50%)
  });

  it("treats a negative slippage as zero rather than reducing the cap", () => {
    expect(applySlippageCeil("1000000", -500)).toBe("1000000");
  });
});

describe("applySlippageFloor", () => {
  it("subtracts the requested tolerance, rounded down", () => {
    expect(applySlippageFloor("1000000", 150)).toBe("985000");
  });

  it("caps redemption tolerance at MAX_SLIPPAGE_BPS", () => {
    expect(applySlippageFloor("1000000", 50_000)).toBe("500000");
  });

  it("treats a negative slippage as zero", () => {
    expect(applySlippageFloor("1000000", -500)).toBe("1000000");
  });
});
