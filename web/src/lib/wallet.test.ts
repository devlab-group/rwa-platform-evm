import { afterEach, describe, expect, it, vi } from "vitest";
import { encodeErrorResult } from "viem";
import { rwaTokenErrorsAbi } from "./abis";
import { describeWalletError, waitForTxReceipt } from "./wallet";

const HASH = "0xaa" as const;

/** A provider that answers just enough for viem to fetch one receipt. */
function installReceiptProvider(status: "0x1" | "0x0") {
  const request = vi.fn(async ({ method }: { method: string }) => {
    switch (method) {
      case "eth_chainId":
        return "0x7a69";
      case "eth_blockNumber":
        return "0x2";
      case "eth_getTransactionReceipt":
        return {
          transactionHash: HASH,
          blockNumber: "0x1",
          blockHash: "0xbb",
          transactionIndex: "0x0",
          from: "0x0000000000000000000000000000000000000001",
          to: "0x0000000000000000000000000000000000000002",
          cumulativeGasUsed: "0x0",
          gasUsed: "0x0",
          effectiveGasPrice: "0x0",
          contractAddress: null,
          logs: [],
          logsBloom: `0x${"0".repeat(512)}`,
          type: "0x2",
          status,
        };
      default:
        throw new Error(`unexpected method ${method}`);
    }
  });
  (window as unknown as { ethereum: unknown }).ethereum = { request };
  return request;
}

afterEach(() => {
  delete (window as unknown as { ethereum?: unknown }).ethereum;
});

describe("waitForTxReceipt", () => {
  it("resolves for a successful transaction", async () => {
    installReceiptProvider("0x1");
    await expect(waitForTxReceipt(HASH)).resolves.toBeUndefined();
  });

  // A reverted transaction still produces a receipt, so without the status
  // check the console would announce a seizure the chain refused.
  it("throws for a mined-but-reverted transaction", async () => {
    installReceiptProvider("0x0");
    await expect(waitForTxReceipt(HASH)).rejects.toThrow(/reverted on-chain/i);
  });
});

describe("describeWalletError", () => {
  it.each([
    ["SystemAddressCannotBeSeized", /can never be seized from/i],
    ["SystemAddressCannotBeFrozen", /can never be frozen/i],
    ["RecipientNotAllowed", /not Allowed in the compliance registry/i],
  ])("names %s from the revert payload", (errorName, expected) => {
    const data = encodeErrorResult({
      abi: rwaTokenErrorsAbi,
      errorName: errorName as "SystemAddressCannotBeSeized",
      args: ["0x0000000000000000000000000000000000000040"],
    });
    expect(
      describeWalletError(
        Object.assign(new Error("execution reverted"), { data }),
      ),
    ).toMatch(expected);
  });

  it("reports the unfrozen amount for a frozen-balance revert", () => {
    const data = encodeErrorResult({
      abi: rwaTokenErrorsAbi,
      errorName: "ERC7943InsufficientUnfrozenBalance",
      args: ["0x0000000000000000000000000000000000000040", 100n, 60n],
    });
    // Nested one level down, the way viem wraps a wallet error.
    const err = Object.assign(new Error("execution reverted"), {
      cause: { data },
    });
    expect(describeWalletError(err)).toMatch(/60 of their balance is movable/);
  });

  it("falls back to the raw message when there is nothing to decode", () => {
    expect(describeWalletError(new Error("User rejected the request."))).toBe(
      "User rejected the request.",
    );
    expect(describeWalletError("not an error at all")).toBe(
      "Transaction failed.",
    );
  });
});
