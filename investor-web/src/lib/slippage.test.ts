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
    // 3 * 1.0001 = 3.0003 -> must round up to 4, never truncate to 3
    expect(applySlippageCeil("3", 1)).toBe("4");
  });

  it("caps an unreasonably large slippage value at MAX_SLIPPAGE_BPS", () => {
    // A fat-fingered/malicious value (1_000_000 bps = 10,000%) must not
    // silently blow up the ERC-20 approve() amount — clamp to the cap
    // instead of trusting raw input.
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

  it("clamps above 10000 bps (100%) rather than going negative", () => {
    expect(applySlippageFloor("1000000", 50_000)).toBe("0");
  });

  it("treats a negative slippage as zero", () => {
    expect(applySlippageFloor("1000000", -500)).toBe("1000000");
  });
});
