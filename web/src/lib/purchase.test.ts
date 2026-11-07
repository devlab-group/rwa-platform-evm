import { describe, expect, it } from "vitest";
import { orderPricingMessage, withdrawableQuote } from "./purchase";

const fmt = (raw: bigint) => `<${raw}>`;

describe("withdrawableQuote", () => {
  it("subtracts open order deposits from the quote balance", () => {
    expect(
      withdrawableQuote({
        quoteBalance: "5000000",
        pendingPurchaseQuote: "1500000",
      }),
    ).toBe(3_500_000n);
  });

  it("is the whole balance when no deposit is held", () => {
    expect(
      withdrawableQuote({ quoteBalance: "5000000", pendingPurchaseQuote: "0" }),
    ).toBe(5_000_000n);
  });

  it("is the whole balance on a Vault that reports no pending deposits", () => {
    expect(withdrawableQuote({ quoteBalance: "5000000" })).toBe(5_000_000n);
  });

  it("is zero when deposits equal the balance", () => {
    expect(
      withdrawableQuote({
        quoteBalance: "5000000",
        pendingPurchaseQuote: "5000000",
      }),
    ).toBe(0n);
  });

  it("never goes negative when a stale balance is below the deposits", () => {
    expect(
      withdrawableQuote({
        quoteBalance: "4000000",
        pendingPurchaseQuote: "5000000",
      }),
    ).toBe(0n);
  });

  it("is unknown while the balance is missing or malformed", () => {
    expect(withdrawableQuote({ pendingPurchaseQuote: "1" })).toBeUndefined();
    expect(withdrawableQuote({ quoteBalance: "" })).toBeUndefined();
    expect(withdrawableQuote({ quoteBalance: "12.5" })).toBeUndefined();
  });

  it("handles balances beyond 2^53 without losing precision", () => {
    expect(
      withdrawableQuote({
        quoteBalance: "9007199254740993000",
        pendingPurchaseQuote: "1000",
      }),
    ).toBe(9_007_199_254_740_992_000n);
  });
});

describe("orderPricingMessage", () => {
  it("names the round and the quote of a priced order", () => {
    expect(
      orderPricingMessage(
        { state: "priced", quoteAmount: 2_500_000n, roundId: 1042n },
        fmt,
      ),
    ).toMatch(/round 1042: <2500000>.*Anyone can settle/);
  });

  it("says a waiting order has no round yet", () => {
    expect(orderPricingMessage({ state: "waiting" }, fmt)).toMatch(
      /Waiting for a price round/,
    );
  });

  it("carries the reason an unusable round gives", () => {
    expect(
      orderPricingMessage({ state: "blocked", message: "feed is stale" }, fmt),
    ).toMatch(/cannot be used: feed is stale/);
  });
});
