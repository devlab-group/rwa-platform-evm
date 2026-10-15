import { describe, expect, it } from "vitest";
import { sha256, toBytes, toFunctionSelector } from "viem";
import { encodeReasonCode, fundBatchCalls } from "./redemption";
import { erc20ApproveCall, escrowCall } from "./wallet";

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
