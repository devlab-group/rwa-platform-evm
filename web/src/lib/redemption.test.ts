import { describe, expect, it, vi } from "vitest";
import { sha256, toBytes, toFunctionSelector } from "viem";
import {
  encodeReasonCode,
  fundBatchCalls,
  fundingStateMessage,
  redemptionQuote,
  redemptionQuoteLabel,
  resolveFundingAmount,
} from "./redemption";
import { erc20ApproveCall, escrowCall, type FundingPreview } from "./wallet";

describe("encodeReasonCode", () => {
  it("passes a 0x + 64-hex bytes32 through unchanged", () => {
    const bytes32 = `0x${"ab".repeat(32)}`;
    expect(encodeReasonCode(bytes32)).toBe(bytes32);
  });

  it("hashes free text with sha256 of its UTF-8 bytes", () => {
    const text = "NON_COMPLIANT";
    expect(encodeReasonCode(text)).toBe(sha256(toBytes(text)));
  });

  it("hashes a 0x string that is not exactly 64 hex chars (treated as text)", () => {
    const notBytes32 = "0xdeadbeef";
    expect(encodeReasonCode(notBytes32)).toBe(sha256(toBytes(notBytes32)));
  });

  it("rejects an empty reason code", () => {
    expect(() => encodeReasonCode("")).toThrow();
  });
});

describe("fundBatchCalls", () => {
  const quoteToken = "0x0000000000000000000000000000000000000070";
  const escrow = "0x0000000000000000000000000000000000000050";

  it("approves exactly the quote amount, funds, then claims the same request", () => {
    expect(fundBatchCalls(quoteToken, escrow, 7n, 100n, true)).toEqual([
      erc20ApproveCall(quoteToken, escrow, 100n),
      escrowCall(escrow, "fundRedemption", 7n),
      escrowCall(escrow, "claimRedemption", 7n),
    ]);
    // Encode the contract's claimRedemption(uint256) selector
    expect(
      escrowCall(escrow, "claimRedemption", 7n).data.startsWith(
        toFunctionSelector("claimRedemption(uint256)"),
      ),
    ).toBe(true);
  });

  it("leaves the claim out when asked", () => {
    expect(fundBatchCalls(quoteToken, escrow, 7n, 100n, false)).toHaveLength(2);
  });
});

// A forward request is one carrying the strategy that prices it; instant requests carry none
const FORWARD = {
  strategy: "0x0000000000000000000000000000000000000060",
  baseRound: "40",
  minQuoteOut: "400000",
};
const fmt = (raw: bigint | string) => `<${raw}>`;

describe("redemptionQuote", () => {
  it("never presents a Pending forward request's placeholder zero as a price", () => {
    const q = redemptionQuote({
      ...FORWARD,
      status: "Pending",
      quoteAmount: "0",
    });
    expect(q).toEqual({ kind: "pricedAtFunding", minQuoteOut: "400000" });
    expect(redemptionQuoteLabel(q, (raw) => `${raw} units`)).toBe(
      "Priced at funding (holder minimum 400000 units)",
    );
  });

  it("says there is no holder minimum when minQuoteOut is 0", () => {
    const label = redemptionQuoteLabel(
      redemptionQuote({
        ...FORWARD,
        minQuoteOut: "0",
        status: "Pending",
        quoteAmount: "0",
      }),
      fmt,
    );
    expect(label).toBe("Priced at funding (no holder minimum)");
  });

  it.each(["Rejected", "Cancelled"] as const)(
    "shows a forward request that ended %s before funding as never priced",
    (status) => {
      const q = redemptionQuote({ ...FORWARD, status, quoteAmount: "0" });
      expect(q).toEqual({ kind: "unpriced" });
      expect(redemptionQuoteLabel(q, fmt)).toBe("Never priced");
    },
  );

  it.each(["Funded", "Completed"] as const)(
    "shows the priced amount of a %s forward request",
    (status) => {
      const q = redemptionQuote({
        ...FORWARD,
        status,
        quoteAmount: "475000",
      });
      expect(q).toEqual({ kind: "amount", amount: "475000" });
      expect(redemptionQuoteLabel(q, fmt)).toBe("<475000>");
    },
  );

  it("shows an instant request's recorded quote as before, even a zero one", () => {
    expect(
      redemptionQuote({ status: "Pending", quoteAmount: "475000" }),
    ).toEqual({ kind: "amount", amount: "475000" });
    expect(redemptionQuote({ status: "Pending", quoteAmount: "0" })).toEqual({
      kind: "amount",
      amount: "0",
    });
  });
});

describe("resolveFundingAmount", () => {
  const pending = { ...FORWARD, status: "Pending" as const, quoteAmount: "0" };

  it("takes a Pending forward request's amount from previewFunding, not from the recorded zero", async () => {
    const read = vi.fn(async (): Promise<FundingPreview> => ({
      state: "priced",
      amount: 475_123n,
    }));
    await expect(resolveFundingAmount(pending, read, fmt)).resolves.toEqual({
      amount: 475_123n,
    });
    expect(read).toHaveBeenCalledTimes(1);
  });

  it("reads previewFunding on every call, so a second funding sees the fresh price", async () => {
    const read = vi
      .fn<() => Promise<FundingPreview>>()
      .mockResolvedValueOnce({ state: "priced", amount: 100n })
      .mockResolvedValueOnce({ state: "priced", amount: 250n });
    expect(await resolveFundingAmount(pending, read, fmt)).toEqual({
      amount: 100n,
    });
    expect(await resolveFundingAmount(pending, read, fmt)).toEqual({
      amount: 250n,
    });
  });

  it.each([
    ["waiting", { state: "waiting" }, /Waiting for a price round/],
    [
      "belowMin",
      { state: "belowMin", quoted: 400n, minQuoteOut: 500n },
      /<400> is below the holder's minimum of <500>.*Reject/,
    ],
    ["closed", { state: "closed" }, /Funding closed.*holder can cancel/],
    [
      "blocked",
      { state: "blocked", message: "feed is stale" },
      /feed is stale/,
    ],
  ] as [string, FundingPreview, RegExp][])(
    "returns an error, not an amount, when previewFunding is %s",
    async (_state, preview, message) => {
      const r = await resolveFundingAmount(pending, async () => preview, fmt);
      expect(r).toEqual({ error: expect.stringMatching(message) });
    },
  );

  it("uses the recorded quote of an instant request and never calls previewFunding", async () => {
    const read = vi.fn();
    await expect(
      resolveFundingAmount(
        { status: "Pending", quoteAmount: "475000" },
        read,
        fmt,
      ),
    ).resolves.toEqual({ amount: 475_000n });
    expect(read).not.toHaveBeenCalled();
  });

  it("reports a missing recorded quote on an instant request", async () => {
    const r = await resolveFundingAmount(
      { status: "Pending", quoteAmount: undefined },
      vi.fn(),
      fmt,
    );
    expect(r).toEqual({ error: expect.stringMatching(/missing/) });
  });

  it("refuses to fund a forward request that was never priced", async () => {
    const read = vi.fn();
    const r = await resolveFundingAmount(
      { ...FORWARD, status: "Cancelled", quoteAmount: "0" },
      read,
      fmt,
    );
    expect(r).toEqual({ error: expect.stringMatching(/never priced/) });
    expect(read).not.toHaveBeenCalled();
  });
});

describe("fundingStateMessage", () => {
  it("states the exact amount funding will pull", () => {
    expect(fundingStateMessage({ state: "priced", amount: 475n }, fmt)).toBe(
      "Funding will pull <475>.",
    );
  });
});
