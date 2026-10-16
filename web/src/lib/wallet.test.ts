import { afterEach, describe, expect, it, vi } from "vitest";
import {
  decodeFunctionData,
  encodeErrorResult,
  encodeFunctionResult,
  type Hex,
} from "viem";
import {
  incomeDistributorAbi,
  incomeDistributorErrorsAbi,
  oracleStrategyErrorsAbi,
  rwaTokenErrorsAbi,
  supplyControllerAbi,
  supplyControllerErrorsAbi,
  vaultAbi,
  vaultErrorsAbi,
} from "./abis";
import {
  burnCall,
  createDistributionCall,
  describeWalletError,
  extendClaimDeadlineCall,
  findOnChainDistribution,
  reclaimUnclaimedCall,
  setMinPurchaseQuoteCall,
  waitForTxReceipt,
} from "./wallet";

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

describe("rwa-v3 revert messages", () => {
  const explain = (
    abi: readonly unknown[],
    errorName: string,
    args?: unknown[],
  ) =>
    describeWalletError(
      Object.assign(new Error("x"), {
        data: encodeErrorResult({
          abi,
          errorName,
          args,
        } as Parameters<typeof encodeErrorResult>[0]),
      }),
    );

  it("explains QuoteBelowMinimum with the quote and the minimum", () => {
    expect(explain(vaultErrorsAbi, "QuoteBelowMinimum", [5n, 25n])).toMatch(
      /quote 5 is below the minimum of 25/,
    );
  });

  it.each([
    ["StalePrice", [1n, 2n], /stale/i],
    ["InvalidPrice", [0n, 1n], /invalid/i],
    ["SequencerDown", [], /sequencer .* down/i],
    ["GracePeriodNotOver", [1n, 2n], /grace period/i],
    ["FeedDecimalsChanged", [8, 6], /decimals/i],
  ])("explains oracle error %s", (name, args, expected) => {
    expect(explain(oracleStrategyErrorsAbi, name, args)).toMatch(expected);
  });

  it.each([
    ["ClaimWindowTooShort", [1n, 2n], /7-day minimum/],
    ["ClaimWindowOpen", [1n, 2n], /still open/],
    ["DeadlineNotExtended", [5n, 4n], /later than the current/],
    ["RecordBlockNotPast", [9n, 9n], /not before the block/],
    ["AlreadyReclaimed", [1n], /already reclaimed/],
    ["ProjectPaused", [], /paused/],
    ["ZeroTotal", [], /zero/],
  ])("explains income error %s", (name, args, expected) => {
    expect(explain(incomeDistributorErrorsAbi, name, args)).toMatch(expected);
  });
});

describe("rwa-v3 call encoders", () => {
  const VAULT = "0x0000000000000000000000000000000000000040";
  const DIST = "0x0000000000000000000000000000000000000080";

  it("encodes Vault.setMinPurchaseQuote", () => {
    const call = setMinPurchaseQuoteCall(VAULT, 25_000_000n);
    expect(call.to).toBe(VAULT);
    const d = decodeFunctionData({ abi: vaultAbi, data: call.data });
    expect(d.functionName).toBe("setMinPurchaseQuote");
    expect(d.args).toEqual([25_000_000n]);
  });

  it("encodes createDistribution, extendClaimDeadline and reclaimUnclaimed against the distributor", () => {
    const create = createDistributionCall(DIST, {
      merkleRoot: `0x${"ab".repeat(32)}`,
      total: 7n,
      recordBlock: 3n,
      claimDeadline: 4_000_000_000n,
      snapshotURI: "ipfs://x",
      snapshotSha256: `0x${"cd".repeat(32)}`,
    });
    const extend = extendClaimDeadlineCall(DIST, 2n, 4_100_000_000n);
    const reclaim = reclaimUnclaimedCall(DIST, 2n);
    for (const c of [create, extend, reclaim]) expect(c.to).toBe(DIST);
    const dec = (data: `0x${string}`) =>
      decodeFunctionData({ abi: incomeDistributorAbi, data });
    expect(dec(extend.data)).toMatchObject({
      functionName: "extendClaimDeadline",
      args: [2n, 4_100_000_000n],
    });
    expect(dec(reclaim.data)).toMatchObject({
      functionName: "reclaimUnclaimed",
      args: [2n],
    });
    expect(dec(create.data).functionName).toBe("createDistribution");
  });
});

describe("findOnChainDistribution", () => {
  const DISTRIBUTOR = "0x0000000000000000000000000000000000000007" as const;
  const ROOT_A = `0x${"aa".repeat(32)}` as Hex;
  const ROOT_B = `0x${"bb".repeat(32)}` as Hex;
  const SHA_A = `0x${"1a".repeat(32)}` as Hex;
  const SHA_B = `0x${"2b".repeat(32)}` as Hex;

  // A wallet on chain 31337 whose IncomeDistributor holds these distributions, by id
  function installDistributor(
    distributions: { merkleRoot: Hex; snapshotSha256: Hex }[],
    chainId = 31337,
  ) {
    const request = vi.fn(
      async ({ method, params }: { method: string; params?: unknown[] }) => {
        if (method === "eth_chainId") return `0x${chainId.toString(16)}`;
        if (method !== "eth_call") throw new Error(`unexpected ${method}`);
        const { data } = params![0] as { data: Hex };
        const call = decodeFunctionData({ abi: incomeDistributorAbi, data });
        if (call.functionName === "nextId") {
          return encodeFunctionResult({
            abi: incomeDistributorAbi,
            functionName: "nextId",
            result: BigInt(distributions.length),
          });
        }
        const d = distributions[Number(call.args![0])];
        return encodeFunctionResult({
          abi: incomeDistributorAbi,
          functionName: "getDistribution",
          result: {
            ...d,
            total: 10n,
            claimed: 0n,
            recordBlock: 5n,
            claimDeadline: 4102444800n,
            reclaimed: false,
          },
        });
      },
    );
    (window as unknown as { ethereum: unknown }).ethereum = { request };
    return request;
  }

  it("returns the id of the distribution that already pays the snapshot", async () => {
    installDistributor([
      { merkleRoot: ROOT_B, snapshotSha256: SHA_B },
      { merkleRoot: ROOT_A, snapshotSha256: SHA_A },
      { merkleRoot: ROOT_B, snapshotSha256: SHA_A },
    ]);
    await expect(
      findOnChainDistribution(
        31337,
        DISTRIBUTOR,
        ROOT_A.toUpperCase().replace("0X", "0x") as Hex,
        SHA_A,
      ),
    ).resolves.toBe(1n);
  });

  it("returns null when no distribution has both the root and the snapshot hash", async () => {
    installDistributor([
      { merkleRoot: ROOT_A, snapshotSha256: SHA_B },
      { merkleRoot: ROOT_B, snapshotSha256: SHA_A },
    ]);
    await expect(
      findOnChainDistribution(31337, DISTRIBUTOR, ROOT_A, SHA_A),
    ).resolves.toBeNull();
  });

  it("returns null for a distributor with no distributions yet", async () => {
    installDistributor([]);
    await expect(
      findOnChainDistribution(31337, DISTRIBUTOR, ROOT_A, SHA_A),
    ).resolves.toBeNull();
  });

  it("refuses to read from a wallet on another chain", async () => {
    const request = installDistributor(
      [{ merkleRoot: ROOT_A, snapshotSha256: SHA_A }],
      1,
    );
    await expect(
      findOnChainDistribution(31337, DISTRIBUTOR, ROOT_A, SHA_A),
    ).rejects.toThrow(/chain/i);
    expect(request.mock.calls.map(([r]) => r.method)).not.toContain("eth_call");
  });
});
