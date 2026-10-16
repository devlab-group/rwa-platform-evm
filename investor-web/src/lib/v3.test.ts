import { describe, expect, it } from "vitest";
import { encodeErrorResult } from "viem";
import { incomeDistributorAbi, oracleErrorsAbi, vaultErrorsAbi } from "./abis";
import { minPurchaseQuote, minPurchaseRefusal, priceAvailability } from "./v3";
import { describeTxError, oracleErrorName } from "./wallet";

const revert = (data: string) => ({ cause: { data } });

describe("minimum purchase", () => {
  it("reads minPurchaseQuote and treats a missing or bad value as no minimum", () => {
    expect(minPurchaseQuote({ minPurchaseQuote: "5000000" } as never)).toBe(
      5000000n,
    );
    expect(minPurchaseQuote({} as never)).toBe(0n);
    expect(minPurchaseQuote({ minPurchaseQuote: "x" } as never)).toBe(0n);
  });

  it("refuses only a quote strictly below the minimum, scaled by quote decimals", () => {
    const msg = minPurchaseRefusal(4999999n, 5000000n, 6);
    expect(msg).toContain("5");
    expect(msg).toContain("below the project minimum");
    expect(minPurchaseRefusal(5000000n, 5000000n, 6)).toBeNull();
    expect(minPurchaseRefusal(1n, 0n, 6)).toBeNull();
  });
});

describe("price availability", () => {
  const oracle = (o: object) =>
    ({ strategyKind: "Oracle", oracle: { available: true, ...o } }) as never;

  it("is available for a fixed project and a healthy oracle", () => {
    expect(
      priceAvailability({ strategyKind: "Fixed" } as never, undefined),
    ).toEqual({ unavailable: false });
    expect(priceAvailability(oracle({}), {} as never).unavailable).toBe(false);
  });

  it("is unavailable when the oracle says so, with its reason", () => {
    const r = priceAvailability(
      oracle({ available: false, error: "StalePrice" }),
      undefined,
    );
    expect(r.unavailable).toBe(true);
    expect(r.reason).toContain("has not updated");
  });

  it("is unavailable on an inventory priceError or a preview oracle error", () => {
    expect(
      priceAvailability(oracle({}), { priceError: "SequencerDown" } as never)
        .reason,
    ).toContain("sequencer is down");
    expect(
      priceAvailability(undefined, undefined, "InvalidPrice").unavailable,
    ).toBe(true);
  });

  it("ignores a stale oracle block on a non-oracle project", () => {
    expect(
      priceAvailability(
        { strategyKind: "Fixed", oracle: { available: false } } as never,
        undefined,
      ).unavailable,
    ).toBe(false);
  });
});

describe("error decoding", () => {
  it("decodes QuoteBelowMinimum", () => {
    const data = encodeErrorResult({
      abi: vaultErrorsAbi,
      errorName: "QuoteBelowMinimum",
      args: [10n, 20n],
    });
    const msg = describeTxError(revert(data), "fallback");
    expect(msg).toContain("below the project minimum");
    expect(msg).toContain("10");
    expect(msg).toContain("20");
  });

  it("decodes oracle errors and exposes their name", () => {
    const data = encodeErrorResult({
      abi: oracleErrorsAbi,
      errorName: "SequencerDown",
    });
    expect(oracleErrorName(revert(data))).toBe("SequencerDown");
    expect(describeTxError(revert(data), "f")).toContain("Prices unavailable");
    expect(oracleErrorName(revert("0xdeadbeef"))).toBeUndefined();
  });

  it("decodes IncomeDistributor errors", () => {
    const data = encodeErrorResult({
      abi: incomeDistributorAbi,
      errorName: "AlreadyClaimed",
      args: [1n, "0x0000000000000000000000000000000000000001"],
    });
    expect(describeTxError(revert(data), "f", "income")).toContain(
      "already been claimed",
    );
  });

  it("uses claim wording for a shared selector only in a claim context", () => {
    const data = encodeErrorResult({
      abi: incomeDistributorAbi,
      errorName: "ProjectPaused",
    });
    expect(describeTxError(revert(data), "f", "income")).toContain(
      "claims resume",
    );
    expect(describeTxError(revert(data), "Buy failed.")).not.toContain("claim");
  });

  it("falls back to the raw message for unknown data", () => {
    expect(describeTxError(new Error("boom"), "f")).toBe("boom");
  });
});
