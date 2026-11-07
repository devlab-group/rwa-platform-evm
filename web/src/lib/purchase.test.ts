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
  // Deposit 4, order minimum 1, all in minimal units
  const bounds = { deposit: 4_000_000n, minQuote: 1_000_000n };
  const priced = (quoteAmount: bigint) =>
    ({ state: "priced", quoteAmount, roundId: 1042n }) as const;

  it("names the round and the quote of an order that will fill", () => {
    expect(orderPricingMessage(priced(2_500_000n), fmt, bounds)).toBe(
      "Priced at round 1042: <2500000>. Anyone can settle it.",
    );
  });

  it("still fills at exactly the deposit and exactly the minimum", () => {
    for (const quote of [4_000_000n, 1_000_000n]) {
      expect(orderPricingMessage(priced(quote), fmt, bounds)).toMatch(
        /Anyone can settle it/,
      );
    }
  });

  it("warns that settling refunds when the quote is above the deposit", () => {
    const msg = orderPricingMessage(priced(5_000_000n), fmt, bounds);
    expect(msg).toMatch(
      /round 1042: <5000000>, above the buyer's deposit of <4000000>/,
    );
    expect(msg).toMatch(/refund the whole deposit.*receives no RWA/);
    expect(msg).not.toMatch(/Anyone can settle it/);
  });

  it("warns that settling refunds when the quote is below the order's own minimum", () => {
    const msg = orderPricingMessage(priced(999_999n), fmt, bounds);
    expect(msg).toMatch(/<999999>, below this order's minimum of <1000000>/);
    expect(msg).toMatch(/refund the whole deposit.*receives no RWA/);
    expect(msg).not.toMatch(/Anyone can settle it/);
  });

  it("checks the minimum recorded with the order, so a zero minimum never refunds a small quote", () => {
    expect(
      orderPricingMessage(priced(1n), fmt, {
        deposit: 4_000_000n,
        minQuote: 0n,
      }),
    ).toMatch(/Anyone can settle it/);
  });

  it("says a waiting order has no round yet", () => {
    expect(orderPricingMessage({ state: "waiting" }, fmt, bounds)).toMatch(
      /Waiting for a price round/,
    );
  });

  it("carries the reason an unusable round gives", () => {
    expect(
      orderPricingMessage(
        { state: "blocked", message: "feed is stale" },
        fmt,
        bounds,
      ),
    ).toMatch(/cannot be used: feed is stale/);
  });
});
