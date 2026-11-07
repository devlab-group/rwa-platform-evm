import { describe, expect, it } from "vitest";
import {
  forwardDeposit,
  formatDuration,
  isForwardPriced,
  orderActions,
  orderReadArgs,
  parseUint,
  redemptionQuoteCell,
  redemptionRefusal,
  roundFromRevert,
  settlementOutlook,
  type OrderContext,
  type OrderRound,
} from "./v4";

describe("isForwardPriced", () => {
  it.each([
    [true, true],
    [false, false],
    [undefined, false],
  ])("forwardPriced %s selects forward: %s", (flag, expected) => {
    expect(isForwardPriced({ forwardPriced: flag })).toBe(expected);
  });

  it("keeps the instant flow before the project loads", () => {
    expect(isForwardPriced(undefined)).toBe(false);
  });
});

describe("redemptionRefusal", () => {
  it("refuses a zero estimate, which could never be paid or funded", () => {
    expect(redemptionRefusal(0n)).toMatch(/estimated quote is zero/);
  });

  it("accepts the smallest non-zero estimate", () => {
    expect(redemptionRefusal(1n)).toBeNull();
  });
});

describe("parseUint", () => {
  it.each(["", " 1", "-1", "1.5", "1e3", "0x10", "abc", "١٢"])(
    "rejects %j",
    (value) => {
      expect(parseUint(value)).toBeNull();
    },
  );

  it("parses a value beyond the safe integer range exactly", () => {
    expect(parseUint("18446744073709551617")).toBe(18446744073709551617n);
  });

  it("treats a missing value as malformed", () => {
    expect(parseUint(undefined)).toBeNull();
  });
});

describe("formatDuration", () => {
  it.each([
    [86_400, "1 day"],
    [172_800, "2 days"],
    [7_200, "2 hours"],
    [3_600, "1 hour"],
    [5_400, "90 minutes"],
    [60, "1 minute"],
    [45, "45 seconds"],
    [1, "1 second"],
    [90_000, "25 hours"],
  ])("%i seconds reads %j", (seconds, text) => {
    expect(formatDuration(seconds)).toBe(text);
  });

  it.each([0, -3600, 1.5, Number.NaN])("returns empty for %s", (seconds) => {
    expect(formatDuration(seconds)).toBe("");
  });
});

describe("forwardDeposit", () => {
  it("adds the slippage margin to the estimate", () => {
    const d = forwardDeposit(1_000_000n, 50, 0n, 6);
    expect(d.deposit).toBe(1_005_000n);
    expect(d.refusal).toBeNull();
  });

  it("rounds the deposit up to the next quote-token unit", () => {
    // 3 * 1.005 = 3.015
    expect(forwardDeposit(3n, 50, 0n, 6).deposit).toBe(4n);
  });

  it("caps the margin at 50 percent", () => {
    expect(forwardDeposit(1_000n, 9_000, 0n, 6).deposit).toBe(1_500n);
  });

  it("accepts a deposit exactly at the minimum", () => {
    const d = forwardDeposit(1_000_000n, 0, 1_000_000n, 6);
    expect(d.refusal).toBeNull();
    expect(d.estimateBelowMinimum).toBe(false);
  });

  it("refuses a deposit one unit under the minimum and names both amounts", () => {
    const d = forwardDeposit(999_999n, 0, 1_000_000n, 6);
    expect(d.refusal).toContain("0.999999");
    expect(d.refusal).toContain("minimum of 1");
  });

  it("lets the margin lift an estimate over the minimum but flags the estimate", () => {
    // Deposit 1_000_970 meets the minimum; at the estimate the settled price would not
    const d = forwardDeposit(995_000n, 60, 1_000_000n, 6);
    expect(d.deposit).toBe(1_000_970n);
    expect(d.refusal).toBeNull();
    expect(d.estimateBelowMinimum).toBe(true);
  });

  it("imposes no minimum when the project sets none", () => {
    const d = forwardDeposit(1n, 0, 0n, 6);
    expect(d.refusal).toBeNull();
    expect(d.estimateBelowMinimum).toBe(false);
  });

  it("refuses a zero deposit, which the Vault rejects as ZeroAmount", () => {
    expect(forwardDeposit(0n, 50, 0n, 6).refusal).toMatch(/zero/);
  });
});

describe("roundFromRevert", () => {
  it("reads RoundNotAvailable as waiting for a price", () => {
    expect(roundFromRevert("RoundNotAvailable")).toEqual({ state: "waiting" });
  });

  it("reads InvalidPrice as an unusable round with the oracle reason", () => {
    expect(roundFromRevert("InvalidPrice")).toEqual({
      state: "unusable",
      reason: "the price feed returned an invalid price",
    });
  });

  it("reads FeedDecimalsChanged as unusable", () => {
    expect(roundFromRevert("FeedDecimalsChanged").state).toBe("unusable");
  });

  it("reads an unrecognized revert as unusable, not as waiting", () => {
    expect(roundFromRevert("Unknown").state).toBe("unusable");
  });
});

describe("orderReadArgs", () => {
  const good = { tokenAmount: "5", baseRound: "100", createdAt: 1_900_000_000 };

  it("converts a well-formed order", () => {
    expect(orderReadArgs(good)).toEqual({
      tokenAmount: 5n,
      baseRound: 100n,
      createdAt: 1_900_000_000n,
    });
  });

  it("accepts the largest uint80 round and rejects the next", () => {
    const max = (2n ** 80n - 1n).toString();
    expect(orderReadArgs({ ...good, baseRound: max })?.baseRound).toBe(
      2n ** 80n - 1n,
    );
    expect(orderReadArgs({ ...good, baseRound: (2n ** 80n).toString() })).toBe(
      null,
    );
  });

  it.each(["", "abc", "-1", "1.5", "1e3", "0x10"])(
    "returns null for a malformed baseRound %j",
    (baseRound) => {
      expect(orderReadArgs({ ...good, baseRound })).toBeNull();
    },
  );

  it("returns null for a malformed token amount", () => {
    expect(orderReadArgs({ ...good, tokenAmount: "five" })).toBeNull();
  });

  it.each([Number.NaN, -1, 1.5, Number.POSITIVE_INFINITY])(
    "returns null for createdAt %s",
    (createdAt) => {
      expect(orderReadArgs({ ...good, createdAt })).toBeNull();
    },
  );
});

describe("settlementOutlook", () => {
  it("fills and refunds the unused deposit when the quote is under it", () => {
    expect(settlementOutlook(900n, 1_000n, 0n)).toEqual({
      outcome: "fill",
      paid: 900n,
      refund: 100n,
    });
  });

  it("fills with no refund when the quote equals the deposit", () => {
    expect(settlementOutlook(1_000n, 1_000n, 0n)).toEqual({
      outcome: "fill",
      paid: 1_000n,
      refund: 0n,
    });
  });

  it("refunds the whole deposit when the quote is one unit above it", () => {
    expect(settlementOutlook(1_001n, 1_000n, 0n)).toEqual({
      outcome: "refund",
      refund: 1_000n,
      reason: "above-deposit",
    });
  });

  it("fills a quote exactly at the minimum", () => {
    expect(settlementOutlook(500n, 1_000n, 500n).outcome).toBe("fill");
  });

  it("refunds the whole deposit when the quote is one unit under the minimum", () => {
    expect(settlementOutlook(499n, 1_000n, 500n)).toEqual({
      outcome: "refund",
      refund: 1_000n,
      reason: "below-minimum",
    });
  });
});

describe("orderActions", () => {
  const TIMEOUT = 1_000_000;
  const pending = { status: "Pending", timeoutAt: TIMEOUT } as const;
  const priced: OrderRound = { state: "priced", quote: 1n, roundId: 7n };
  const waiting: OrderRound = { state: "waiting" };
  const unusable: OrderRound = { state: "unusable", reason: "x" };
  const ctx = (over: Partial<OrderContext> = {}): OrderContext => ({
    nowSeconds: TIMEOUT + 10,
    paused: false,
    buyerAllowed: true,
    isBuyer: true,
    ...over,
  });

  it("offers Settle and no Cancel when a round prices the order, even after the timeout", () => {
    expect(orderActions(pending, priced, ctx())).toEqual({
      settle: true,
      cancel: false,
      note: null,
    });
  });

  it("withholds Settle while the project is paused and says why", () => {
    expect(orderActions(pending, priced, ctx({ paused: true }))).toEqual({
      settle: false,
      cancel: false,
      note: "paused",
    });
  });

  it("still allows Cancel while paused when no round exists", () => {
    expect(orderActions(pending, waiting, ctx({ paused: true })).cancel).toBe(
      true,
    );
  });

  it("holds Cancel back one second before the timeout", () => {
    expect(
      orderActions(pending, waiting, ctx({ nowSeconds: TIMEOUT - 1 })),
    ).toEqual({ settle: false, cancel: false, note: "cancel-opens-later" });
  });

  it("opens Cancel exactly at the timeout", () => {
    expect(
      orderActions(pending, waiting, ctx({ nowSeconds: TIMEOUT })),
    ).toEqual({ settle: false, cancel: true, note: null });
  });

  it("opens Cancel after the timeout when the pricing round is unusable", () => {
    expect(orderActions(pending, unusable, ctx())).toEqual({
      settle: false,
      cancel: true,
      note: null,
    });
  });

  it("never offers Settle without a usable round", () => {
    expect(orderActions(pending, waiting, ctx()).settle).toBe(false);
    expect(orderActions(pending, unusable, ctx()).settle).toBe(false);
  });

  it("offers Cancel only to the buyer", () => {
    expect(orderActions(pending, waiting, ctx({ isBuyer: false }))).toEqual({
      settle: false,
      cancel: false,
      note: "not-buyer",
    });
  });

  it.each([
    ["priced", priced],
    ["waiting", waiting],
  ])(
    "offers nothing and explains when the buyer is not Allowed (%s)",
    (_name, round) => {
      expect(
        orderActions(pending, round, ctx({ buyerAllowed: false })),
      ).toEqual({ settle: false, cancel: false, note: "buyer-not-allowed" });
    },
  );

  it("leaves the decision to the contract when the Allowed lookup failed", () => {
    expect(
      orderActions(pending, priced, ctx({ buyerAllowed: undefined })).settle,
    ).toBe(true);
  });

  it("offers nothing for an unreadable round and says to refresh", () => {
    expect(orderActions(pending, { state: "unknown" }, ctx())).toEqual({
      settle: false,
      cancel: false,
      note: "unreadable",
    });
  });

  it.each([Number.NaN, undefined as unknown as number])(
    "never opens Cancel on a malformed timeout %s",
    (timeoutAt) => {
      expect(
        orderActions({ status: "Pending", timeoutAt }, waiting, ctx()).cancel,
      ).toBe(false);
    },
  );

  it.each(["Settled", "Refunded", "Cancelled", "Rejected"] as const)(
    "offers nothing on a %s order",
    (status) => {
      expect(
        orderActions({ status, timeoutAt: TIMEOUT }, priced, ctx()),
      ).toEqual({ settle: false, cancel: false, note: null });
    },
  );
});

describe("redemptionQuoteCell", () => {
  it("shows a Pending forward request as priced at funding with its minimum, never its stored zero", () => {
    expect(
      redemptionQuoteCell({
        status: "Pending",
        quoteAmount: "0",
        strategy: "0xabc",
        minQuoteOut: "4975000",
      }),
    ).toEqual({ kind: "atFunding", minQuoteOut: "4975000" });
  });

  it("treats a Pending zero quote as awaiting its price even without the strategy field", () => {
    expect(
      redemptionQuoteCell({ status: "Pending", quoteAmount: "0" }),
    ).toEqual({ kind: "atFunding", minQuoteOut: null });
  });

  it("shows a Pending forward request with no quote reported as awaiting its price", () => {
    expect(
      redemptionQuoteCell({ status: "Pending", strategy: "0xabc" }),
    ).toEqual({ kind: "atFunding", minQuoteOut: null });
  });

  it("drops a malformed minimum instead of showing it", () => {
    expect(
      redemptionQuoteCell({
        status: "Pending",
        quoteAmount: "0",
        strategy: "0xabc",
        minQuoteOut: "lots",
      }),
    ).toEqual({ kind: "atFunding", minQuoteOut: null });
  });

  it("shows an instant Pending request's snapshotted quote as an amount", () => {
    expect(
      redemptionQuoteCell({ status: "Pending", quoteAmount: "5000000" }),
    ).toEqual({ kind: "amount", raw: "5000000" });
  });

  it("shows the real quote of a funded forward request", () => {
    expect(
      redemptionQuoteCell({
        status: "Funded",
        quoteAmount: "5100000",
        strategy: "0xabc",
        minQuoteOut: "4975000",
      }),
    ).toEqual({ kind: "amount", raw: "5100000" });
  });

  it.each(["Cancelled", "Rejected"] as const)(
    "shows nothing for a %s forward request, which was never priced",
    (status) => {
      expect(
        redemptionQuoteCell({ status, quoteAmount: "0", strategy: "0xabc" }),
      ).toEqual({ kind: "none" });
    },
  );

  it("passes a malformed amount through for display instead of throwing", () => {
    expect(
      redemptionQuoteCell({ status: "Pending", quoteAmount: "lots" }),
    ).toEqual({ kind: "amount", raw: "lots" });
  });
});
