import { afterEach, describe, expect, it, vi } from "vitest";
import { decodeFunctionData, encodeErrorResult } from "viem";
import {
  rwaTokenErrorsAbi,
  supplyControllerAbi,
  supplyControllerErrorsAbi,
} from "./abis";
import { burnCall, describeWalletError, waitForTxReceipt } from "./wallet";

const HASH = "0xaa" as const;

// A provider that answers just enough for viem to fetch one receipt
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

  // A mined revert still has a receipt; check status before announcing a successful seizure
  it("throws for a mined-but-reverted transaction", async () => {
    installReceiptProvider("0x0");
    await expect(waitForTxReceipt(HASH)).rejects.toThrow(/reverted on-chain/i);
  });
});

describe("describeWalletError", () => {
  it.each([
    ["SystemAddressCannotBeSeized", /cannot be a seizure source/i],
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

describe("burnCall", () => {
  const controller = "0x0000000000000000000000000000000000000044";
  const attestation = {
    auditor: "0x9999999999999999999999999999999999999999",
    profileDigest: `0x${"cc".repeat(32)}`,
    operationId: `0x${"11".repeat(32)}`,
    metadataDigest: `0x${"aa".repeat(32)}`,
    amount: 5n,
    nonce: 7n,
    validUntil: 4102444800n,
    vault: "0x0000000000000000000000000000000000000055",
  } as const;
  const signature = `0x${"ab".repeat(65)}` as const;

  it("encodes the attestation tuple and signature for SupplyController.burn", () => {
    const call = burnCall(controller, attestation, signature);
    expect(call.to).toBe(controller);
    const { functionName, args } = decodeFunctionData({
      abi: supplyControllerAbi,
      data: call.data,
    });
    expect(functionName).toBe("burn");
    expect(args).toEqual([attestation, signature]);
  });

  it("explains insufficient Vault inventory", () => {
    const data = encodeErrorResult({
      abi: supplyControllerErrorsAbi,
      errorName: "InsufficientVaultInventory",
      args: [500n, 20n],
    });
    const msg = describeWalletError(Object.assign(new Error("x"), { data }));
    expect(msg).toMatch(/Vault holds 20 .* less than the 500/);
  });

  it("explains a reused operation ID", () => {
    const data = encodeErrorResult({
      abi: supplyControllerErrorsAbi,
      errorName: "OperationIdAlreadyUsed",
      args: [`0x${"11".repeat(32)}`],
    });
    expect(
      describeWalletError(Object.assign(new Error("x"), { data })),
    ).toMatch(/already executed/);
  });
});
