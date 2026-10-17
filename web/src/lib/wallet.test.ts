import { afterEach, describe, expect, it, vi } from "vitest";
import {
  decodeFunctionData,
  encodeErrorResult,
  erc20Abi,
  encodeFunctionResult,
  type Hex,
} from "viem";
import {
  accessControlAdminAbi,
  erc7943Abi,
  fixedPriceStrategyAbi,
  incomeDistributorAbi,
  pausableAbi,
  redemptionEscrowAbi,
  incomeDistributorErrorsAbi,
  oracleStrategyErrorsAbi,
  rwaTokenErrorsAbi,
  supplyControllerAbi,
  supplyControllerErrorsAbi,
  vaultAbi,
  vaultErrorsAbi,
} from "./abis";
import {
  acceptAdminTransferCall,
  beginAdminTransferCall,
  burnCall,
  createDistributionCall,
  erc20ApproveCall,
  escrowCall,
  describeWalletError,
  extendClaimDeadlineCall,
  findOnChainDistribution,
  forcedTransferCall,
  pauseCall,
  reclaimUnclaimedCall,
  rejectRedemptionCall,
  roleChangeCall,
  sendAcceptAdminTransfer,
  sendBeginAdminTransfer,
  sendBurn,
  sendCreateDistribution,
  sendDeployProject,
  sendErc20Approve,
  sendExtendClaimDeadline,
  sendForcedTransfer,
  sendFundRedemption,
  sendMint,
  sendReclaimUnclaimed,
  sendRejectRedemption,
  sendRoleChange,
  sendSetFrozenTokens,
  sendSetMinPurchaseQuote,
  sendSetPaused,
  sendSetStrategyPrice,
  sendWithdrawProceeds,
  setFrozenTokensCall,
  setMinPurchaseQuoteCall,
  strategyPriceCall,
  waitForTxReceipt,
  withdrawProceedsCall,
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

// Addresses use digits only so viem's checksummed decode output equals the literal
describe("admin call encoders", () => {
  const TOKEN = "0x0000000000000000000000000000000000000010";
  const HOLDER = "0x0000000000000000000000000000000000000021";
  const RECIPIENT = "0x0000000000000000000000000000000000000032";
  const VAULT = "0x0000000000000000000000000000000000000040";
  const ESCROW = "0x0000000000000000000000000000000000000050";
  const STRATEGY = "0x0000000000000000000000000000000000000060";
  const ROLE = `0x${"11".repeat(32)}` as const;
  const REASON = `0x${"22".repeat(32)}` as const;

  it("encodes forcedTransfer with holder before recipient", () => {
    const call = forcedTransferCall(TOKEN, HOLDER, RECIPIENT, 500n);
    expect(call.to).toBe(TOKEN);
    expect(decodeFunctionData({ abi: erc7943Abi, data: call.data })).toEqual({
      functionName: "forcedTransfer",
      args: [HOLDER, RECIPIENT, 500n],
    });
  });

  it("encodes setFrozenTokens with the account and the absolute amount", () => {
    const call = setFrozenTokensCall(TOKEN, HOLDER, 75n);
    expect(call.to).toBe(TOKEN);
    expect(decodeFunctionData({ abi: erc7943Abi, data: call.data })).toEqual({
      functionName: "setFrozenTokens",
      args: [HOLDER, 75n],
    });
  });

  it.each([
    ["grant", "grantRole"],
    ["revoke", "revokeRole"],
  ] as const)(
    "encodes a %s as %s(role, account) on the contract",
    (action, fn) => {
      const call = roleChangeCall(VAULT, action, ROLE, HOLDER);
      expect(call.to).toBe(VAULT);
      expect(
        decodeFunctionData({ abi: accessControlAdminAbi, data: call.data }),
      ).toEqual({ functionName: fn, args: [ROLE, HOLDER] });
    },
  );

  it("encodes beginDefaultAdminTransfer with the new admin", () => {
    const call = beginAdminTransferCall(VAULT, RECIPIENT);
    expect(call.to).toBe(VAULT);
    expect(
      decodeFunctionData({ abi: accessControlAdminAbi, data: call.data }),
    ).toEqual({ functionName: "beginDefaultAdminTransfer", args: [RECIPIENT] });
  });

  it("encodes acceptDefaultAdminTransfer without arguments", () => {
    const call = acceptAdminTransferCall(VAULT);
    expect(call.to).toBe(VAULT);
    expect(
      decodeFunctionData({ abi: accessControlAdminAbi, data: call.data }),
    ).toEqual({ functionName: "acceptDefaultAdminTransfer", args: undefined });
  });

  it.each([
    [true, "pause"],
    [false, "unpause"],
  ] as const)("encodes pause=%s as %s() on the token", (pause, fn) => {
    const call = pauseCall(TOKEN, pause);
    expect(call.to).toBe(TOKEN);
    expect(decodeFunctionData({ abi: pausableAbi, data: call.data })).toEqual({
      functionName: fn,
      args: undefined,
    });
  });

  it.each([
    ["purchase", "setPurchasePrice"],
    ["redemption", "setRedemptionPrice"],
  ] as const)("encodes the %s price as %s on the strategy", (side, fn) => {
    const call = strategyPriceCall(STRATEGY, side, 2_500_000n);
    expect(call.to).toBe(STRATEGY);
    expect(
      decodeFunctionData({ abi: fixedPriceStrategyAbi, data: call.data }),
    ).toEqual({ functionName: fn, args: [2_500_000n] });
  });

  it("encodes withdrawProceeds with the exact amount", () => {
    const call = withdrawProceedsCall(VAULT, 123_456n);
    expect(call.to).toBe(VAULT);
    expect(decodeFunctionData({ abi: vaultAbi, data: call.data })).toEqual({
      functionName: "withdrawProceeds",
      args: [123_456n],
    });
  });

  it.each(["fundRedemption", "claimRedemption"] as const)(
    "encodes escrowCall %s with the redemption id",
    (fn) => {
      const call = escrowCall(ESCROW, fn, 9n);
      expect(call.to).toBe(ESCROW);
      expect(
        decodeFunctionData({ abi: redemptionEscrowAbi, data: call.data }),
      ).toEqual({ functionName: fn, args: [9n] });
    },
  );

  it("encodes rejectRedemption with the id then the bytes32 reason", () => {
    const call = rejectRedemptionCall(ESCROW, 9n, REASON);
    expect(call.to).toBe(ESCROW);
    expect(
      decodeFunctionData({ abi: redemptionEscrowAbi, data: call.data }),
    ).toEqual({ functionName: "rejectRedemption", args: [9n, REASON] });
  });

  it("encodes ERC-20 approve(spender, amount) on the token", () => {
    const call = erc20ApproveCall(TOKEN, ESCROW, 475_000n);
    expect(call.to).toBe(TOKEN);
    expect(decodeFunctionData({ abi: erc20Abi, data: call.data })).toEqual({
      functionName: "approve",
      args: [ESCROW, 475_000n],
    });
  });
});

// Capture eth_sendTransaction to check send* helpers that screen tests stub
describe("wallet broadcast helpers", () => {
  const FROM = "0x0000000000000000000000000000000000000001";
  const TOKEN = "0x0000000000000000000000000000000000000010";
  const VAULT = "0x0000000000000000000000000000000000000040";
  const ESCROW = "0x0000000000000000000000000000000000000050";
  const REASON = `0x${"22".repeat(32)}` as const;

  function installSendProvider() {
    const sent: { from: string; to: string; data: Hex }[] = [];
    const request = vi.fn(
      async ({ method, params }: { method: string; params?: unknown[] }) => {
        if (method === "eth_chainId") return "0x7a69";
        if (method === "eth_call") return "0x"; // the pre-send simulation succeeds
        if (method === "eth_sendTransaction") {
          sent.push(params![0] as (typeof sent)[number]);
          return `0x${"ab".repeat(32)}`;
        }
        throw new Error(`unexpected method ${method}`);
      },
    );
    (window as unknown as { ethereum: unknown }).ethereum = { request };
    return sent;
  }

  it("sendFundRedemption broadcasts fundRedemption(id), never claimRedemption", async () => {
    const sent = installSendProvider();
    await sendFundRedemption(31337, FROM, ESCROW, 7n);
    expect(sent).toHaveLength(1);
    expect(sent[0].from).toBe(FROM);
    expect(sent[0].to.toLowerCase()).toBe(ESCROW);
    expect(
      decodeFunctionData({ abi: redemptionEscrowAbi, data: sent[0].data }),
    ).toEqual({ functionName: "fundRedemption", args: [7n] });
  });

  it("sendRejectRedemption broadcasts rejectRedemption(id, reason)", async () => {
    const sent = installSendProvider();
    await sendRejectRedemption(31337, FROM, ESCROW, 7n, REASON);
    expect(sent[0].to.toLowerCase()).toBe(ESCROW);
    expect(
      decodeFunctionData({ abi: redemptionEscrowAbi, data: sent[0].data }),
    ).toEqual({ functionName: "rejectRedemption", args: [7n, REASON] });
  });

  it("sendWithdrawProceeds broadcasts the exact amount to the vault", async () => {
    const sent = installSendProvider();
    await sendWithdrawProceeds(31337, FROM, VAULT, 1_000n);
    expect(sent[0].to.toLowerCase()).toBe(VAULT);
    expect(decodeFunctionData({ abi: vaultAbi, data: sent[0].data })).toEqual({
      functionName: "withdrawProceeds",
      args: [1_000n],
    });
  });

  it("sendErc20Approve broadcasts approve(spender, amount) to the token", async () => {
    const sent = installSendProvider();
    await sendErc20Approve(31337, FROM, TOKEN, ESCROW, 475_000n);
    expect(sent[0].to.toLowerCase()).toBe(TOKEN);
    expect(decodeFunctionData({ abi: erc20Abi, data: sent[0].data })).toEqual({
      functionName: "approve",
      args: [ESCROW, 475_000n],
    });
  });

  it("sendMint broadcasts SupplyController.mint(attestation, signature)", async () => {
    const sent = installSendProvider();
    const attestation = {
      auditor: "0x0000000000000000000000000000000000000099",
      profileDigest: `0x${"cc".repeat(32)}`,
      recordKey: `0x${"11".repeat(32)}`,
      metadataDigest: `0x${"aa".repeat(32)}`,
      amount: 5n,
      nonce: 7n,
      validUntil: 4102444800n,
      vault: VAULT,
    } as const;
    const signature = `0x${"ab".repeat(65)}` as const;
    const controller = "0x0000000000000000000000000000000000000044";
    await sendMint(31337, FROM, controller, attestation, signature);
    expect(sent[0].to.toLowerCase()).toBe(controller);
    expect(
      decodeFunctionData({ abi: supplyControllerAbi, data: sent[0].data }),
    ).toEqual({ functionName: "mint", args: [attestation, signature] });
  });
});

// Simulation reverts must stop writes before the wallet prompt
describe("simulation before broadcast", () => {
  const FROM = "0x0000000000000000000000000000000000000001" as const;
  const CONTROLLER = "0x0000000000000000000000000000000000000044" as const;
  const attestation = {
    auditor: "0x0000000000000000000000000000000000000099",
    profileDigest: `0x${"cc".repeat(32)}`,
    recordKey: `0x${"11".repeat(32)}`,
    metadataDigest: `0x${"aa".repeat(32)}`,
    amount: 5n,
    nonce: 7n,
    validUntil: 4102444800n,
    vault: "0x0000000000000000000000000000000000000040",
  } as const;
  const signature = `0x${"ab".repeat(65)}` as const;

  // Records each wallet request in order; eth_call answers with callResult or throws callError
  function installSimulatingProvider(callError?: unknown) {
    const requests: { method: string; params?: unknown[] }[] = [];
    (window as unknown as { ethereum: unknown }).ethereum = {
      request: vi.fn(async (req: { method: string; params?: unknown[] }) => {
        requests.push(req);
        if (req.method === "eth_chainId") return "0x7a69";
        if (req.method === "eth_call") {
          if (callError) throw callError;
          return "0x";
        }
        if (req.method === "eth_sendTransaction") return `0x${"ab".repeat(32)}`;
        throw new Error(`unexpected method ${req.method}`);
      }),
    };
    return requests;
  }

  it("simulates the exact transaction from the sender before sending it", async () => {
    const requests = installSimulatingProvider();
    await sendMint(31337, FROM, CONTROLLER, attestation, signature);
    const methods = requests.map((r) => r.method);
    expect(methods.indexOf("eth_call")).toBeGreaterThanOrEqual(0);
    expect(methods.indexOf("eth_call")).toBeLessThan(
      methods.indexOf("eth_sendTransaction"),
    );
    const simulated = requests.find((r) => r.method === "eth_call")!
      .params![0] as { from: string; to: string; data: Hex };
    const sent = requests.find((r) => r.method === "eth_sendTransaction")!
      .params![0] as { from: string; to: string; data: Hex };
    expect({
      from: simulated.from.toLowerCase(),
      to: simulated.to.toLowerCase(),
      data: simulated.data,
    }).toEqual({
      from: sent.from.toLowerCase(),
      to: sent.to.toLowerCase(),
      data: sent.data,
    });
    expect(
      decodeFunctionData({ abi: supplyControllerAbi, data: simulated.data }),
    ).toEqual({ functionName: "mint", args: [attestation, signature] });
  });

  const invalidSignature = encodeErrorResult({
    abi: supplyControllerErrorsAbi,
    errorName: "InvalidSignature",
  });
  it.each([
    [
      "a flat revert (code 3)",
      { code: 3, message: "execution reverted", data: invalidSignature },
      "Transaction simulation failed: The signature does not match this attestation's auditor. Nothing was sent.",
    ],
    [
      "a MetaMask-style nested revert",
      {
        code: -32603,
        message: "Internal JSON-RPC error.",
        data: {
          code: 3,
          message: "execution reverted",
          data: invalidSignature,
        },
      },
      "Transaction simulation failed: The signature does not match this attestation's auditor.",
    ],
    [
      "a revert no pinned ABI knows",
      { code: 3, message: "execution reverted", data: "0xdeadbeef" },
      "Transaction simulation failed: 0xdeadbeef. Nothing was sent.",
    ],
  ])(
    "names the contract error from %s and sends nothing",
    async (_shape, callError, message) => {
      const requests = installSimulatingProvider(callError);
      await expect(
        sendMint(31337, FROM, CONTROLLER, attestation, signature),
      ).rejects.toThrow(message);
      expect(requests.map((r) => r.method)).not.toContain(
        "eth_sendTransaction",
      );
    },
  );

  it("names an error without console wording by its signature and arguments", async () => {
    installSimulatingProvider({
      code: 3,
      message: "execution reverted",
      data: encodeErrorResult({
        abi: rwaTokenErrorsAbi,
        errorName: "ERC7943CannotSend",
        args: [FROM],
      }),
    });
    await expect(
      sendMint(31337, FROM, CONTROLLER, attestation, signature),
    ).rejects.toThrow(
      `Transaction simulation failed: ERC7943CannotSend(${FROM}). Nothing was sent.`,
    );
  });
});

// Every browser-broadcast write must refuse a wallet on another chain before anything is sent
describe("writes on the wrong chain", () => {
  const FROM = "0x0000000000000000000000000000000000000001" as const;
  const A = (n: number) => `0x${n.toString(16).padStart(40, "0")}` as const;
  const B32 = (h: string) => `0x${h.repeat(32)}` as const;
  const attestation = {
    auditor: A(0x99),
    profileDigest: B32("cc"),
    metadataDigest: B32("aa"),
    amount: 5n,
    nonce: 7n,
    validUntil: 4102444800n,
    vault: A(0x55),
  } as const;
  const signature = `0x${"ab".repeat(65)}` as Hex;
  const V2_CONFIG = {
    name: "Acme",
    symbol: "ACME",
    decimals: 18,
    profileDigest: B32("11"),
    projectId: B32("22"),
    quoteToken: A(2),
    purchasePricePerWholeToken: 1n,
    redemptionPricePerWholeToken: 2n,
    redemptionTimeout: 604800n,
    admin: A(3),
    auditor: A(4),
    complianceOperator: A(5),
    pricer: A(6),
    treasurer: A(7),
    redemptionManager: A(8),
    treasury: A(9),
    adminTransferDelay: 172800,
  };

  const writes: Array<[string, () => Promise<Hex>]> = [
    ["deploy", () => sendDeployProject(31337, FROM, A(1), V2_CONFIG)],
    [
      "mint",
      () =>
        sendMint(
          31337,
          FROM,
          A(0x44),
          { ...attestation, recordKey: B32("11") },
          signature,
        ),
    ],
    [
      "burn",
      () =>
        sendBurn(
          31337,
          FROM,
          A(0x44),
          { ...attestation, operationId: B32("11") },
          signature,
        ),
    ],
    ["approve", () => sendErc20Approve(31337, FROM, A(0x10), A(0x50), 1n)],
    ["pause", () => sendSetPaused(31337, FROM, A(0x10), true)],
    ["unpause", () => sendSetPaused(31337, FROM, A(0x10), false)],
    [
      "setMinPurchaseQuote",
      () => sendSetMinPurchaseQuote(31337, FROM, A(0x40), 1n),
    ],
    [
      "setPrice",
      () => sendSetStrategyPrice(31337, FROM, A(0x60), "purchase", 1n),
    ],
    [
      "setFrozenTokens",
      () => sendSetFrozenTokens(31337, FROM, A(0x10), A(0x21), 1n),
    ],
    [
      "forcedTransfer",
      () => sendForcedTransfer(31337, FROM, A(0x10), A(0x21), A(0x32), 1n),
    ],
    [
      "grantRole",
      () => sendRoleChange(31337, FROM, A(0x10), "grant", B32("11"), A(0x21)),
    ],
    [
      "revokeRole",
      () => sendRoleChange(31337, FROM, A(0x10), "revoke", B32("11"), A(0x21)),
    ],
    [
      "beginAdminTransfer",
      () => sendBeginAdminTransfer(31337, FROM, A(0x10), A(0x21)),
    ],
    [
      "acceptAdminTransfer",
      () => sendAcceptAdminTransfer(31337, FROM, A(0x10)),
    ],
    ["withdrawProceeds", () => sendWithdrawProceeds(31337, FROM, A(0x40), 1n)],
    ["fundRedemption", () => sendFundRedemption(31337, FROM, A(0x50), 1n)],
    [
      "rejectRedemption",
      () => sendRejectRedemption(31337, FROM, A(0x50), 1n, B32("22")),
    ],
    [
      "createDistribution",
      () =>
        sendCreateDistribution(31337, FROM, A(0x80), {
          merkleRoot: B32("ab"),
          total: 7n,
          recordBlock: 3n,
          claimDeadline: 4_000_000_000n,
          snapshotURI: "ipfs://x",
          snapshotSha256: B32("cd"),
        }),
    ],
    [
      "extendClaimDeadline",
      () => sendExtendClaimDeadline(31337, FROM, A(0x80), 2n, 4_100_000_000n),
    ],
    ["reclaimUnclaimed", () => sendReclaimUnclaimed(31337, FROM, A(0x80), 2n)],
  ];

  it.each(writes)(
    "%s rejects with viem ChainMismatchError and sends nothing",
    async (_name, send) => {
      const sent: unknown[] = [];
      // The wallet is on chain 1; the project is on 31337
      (window as unknown as { ethereum: unknown }).ethereum = {
        request: vi.fn(
          async ({
            method,
            params,
          }: {
            method: string;
            params?: unknown[];
          }) => {
            if (method === "eth_chainId") return "0x1";
            if (method === "eth_sendTransaction") {
              sent.push(params);
              return `0x${"ab".repeat(32)}`;
            }
            throw new Error(`unexpected method ${method}`);
          },
        ),
      };
      await expect(send()).rejects.toMatchObject({
        cause: { name: "ChainMismatchError" },
      });
      expect(sent).toEqual([]);
    },
  );
});
