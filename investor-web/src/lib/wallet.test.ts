import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  decodeFunctionData,
  encodeErrorResult,
  encodeFunctionResult,
  parseAbi,
  type Address,
  type Hex,
} from "viem";
import { installFakeWallet } from "../test/walletHarness";
import {
  describeTxError,
  readOrderRound,
  sendBuy,
  sendCancelPurchase,
  sendCancelRedemption,
  sendClaimRedemption,
  sendErc20Approve,
  sendErc20Transfer,
  sendIncomeClaim,
  sendRequestPurchase,
  sendRequestRedemption,
  sendSettlePurchase,
} from "./wallet";

// Independent of src/lib/abis.ts: signatures spelled out here
const abi = parseAbi([
  "function buy(uint256 tokenAmount, uint256 maxQuoteAmount, address recipient, uint64 deadline)",
  "function requestRedemption(uint256 rwaAmount, uint256 minQuoteOut, uint64 deadline)",
  "function claimRedemption(uint256 id)",
  "function cancelRedemption(uint256 id)",
  "function transfer(address to, uint256 amount)",
  "function approve(address spender, uint256 amount)",
  "function claim(uint256 id, uint256 amount, bytes32[] proof)",
  "function requestPurchase(uint256 tokenAmount, uint256 maxQuoteAmount, address recipient, uint64 deadline) returns (uint256 id)",
  "function settlePurchase(uint256 id)",
  "function cancelPurchase(uint256 id)",
  "function quotePurchaseAfter(uint256 tokenAmount, uint80 baseRound, uint64 orderTime) view returns (uint256 quoteAmount, uint80 roundId)",
]);

const CHAIN = 31337;
const OWNER = "0x1111111111111111111111111111111111111111" as Address;
const VAULT = "0x2222222222222222222222222222222222222222" as Address;
const ESCROW = "0x3333333333333333333333333333333333333333" as Address;
const TOKEN = "0x4444444444444444444444444444444444444444" as Address;
const OTHER = "0x5555555555555555555555555555555555555555" as Address;
const DISTRIBUTOR = "0x6666666666666666666666666666666666666666" as Address;
const PROOF: Hex[] = [`0x${"aa".repeat(32)}`, `0x${"bb".repeat(32)}`];

let wallet: ReturnType<typeof installFakeWallet>;

beforeEach(() => {
  wallet = installFakeWallet({ account: OWNER, chainId: CHAIN });
});
afterEach(() => wallet.uninstall());

// The one transaction sent, with its calldata decoded against the independent ABI
function onlySent() {
  expect(wallet.sentTransactions).toHaveLength(1);
  const tx = wallet.sentTransactions[0];
  const decoded = decodeFunctionData({ abi, data: tx.data as Hex });
  return { to: tx.to?.toLowerCase(), from: tx.from?.toLowerCase(), decoded };
}

describe("investor transaction encoding", () => {
  it("encodes Vault.buy with every argument in order and targets the vault", async () => {
    const hash = await sendBuy(CHAIN, OWNER, VAULT, {
      tokenAmount: 5n,
      maxQuoteAmount: 7n,
      recipient: OTHER,
      deadline: 1_900_000_000n,
    });
    expect(hash).toBe(wallet.sentTxHash);
    const { to, from, decoded } = onlySent();
    expect(to).toBe(VAULT);
    expect(from).toBe(OWNER);
    expect(decoded.functionName).toBe("buy");
    expect(decoded.args).toEqual([5n, 7n, OTHER, 1_900_000_000n]);
  });

  it("encodes requestRedemption as (rwaAmount, minQuoteOut, deadline) to the escrow", async () => {
    await sendRequestRedemption(CHAIN, OWNER, ESCROW, {
      rwaAmount: 11n,
      minQuoteOut: 13n,
      deadline: 1_900_000_001n,
    });
    const { to, decoded } = onlySent();
    expect(to).toBe(ESCROW);
    expect(decoded.functionName).toBe("requestRedemption");
    expect(decoded.args).toEqual([11n, 13n, 1_900_000_001n]);
  });

  it("encodes claimRedemption(id) to the escrow", async () => {
    await sendClaimRedemption(CHAIN, OWNER, ESCROW, 42n);
    const { to, decoded } = onlySent();
    expect(to).toBe(ESCROW);
    expect(decoded.functionName).toBe("claimRedemption");
    expect(decoded.args).toEqual([42n]);
  });

  it("encodes cancelRedemption(id) to the escrow, not claimRedemption", async () => {
    await sendCancelRedemption(CHAIN, OWNER, ESCROW, 43n);
    const { to, decoded } = onlySent();
    expect(to).toBe(ESCROW);
    expect(decoded.functionName).toBe("cancelRedemption");
    expect(decoded.args).toEqual([43n]);
  });

  it("encodes Vault.requestPurchase with every argument in order and targets the vault", async () => {
    await sendRequestPurchase(CHAIN, OWNER, VAULT, {
      tokenAmount: 5n,
      maxQuoteAmount: 7n,
      recipient: OTHER,
      deadline: 1_900_000_000n,
    });
    const { to, from, decoded } = onlySent();
    expect(to).toBe(VAULT);
    expect(from).toBe(OWNER);
    expect(decoded.functionName).toBe("requestPurchase");
    expect(decoded.args).toEqual([5n, 7n, OTHER, 1_900_000_000n]);
  });

  it("encodes settlePurchase(id) to the vault", async () => {
    await sendSettlePurchase(CHAIN, OWNER, VAULT, 8n);
    const { to, decoded } = onlySent();
    expect(to).toBe(VAULT);
    expect(decoded.functionName).toBe("settlePurchase");
    expect(decoded.args).toEqual([8n]);
  });

  it("encodes cancelPurchase(id) to the vault, not settlePurchase", async () => {
    await sendCancelPurchase(CHAIN, OWNER, VAULT, 9n);
    const { to, decoded } = onlySent();
    expect(to).toBe(VAULT);
    expect(decoded.functionName).toBe("cancelPurchase");
    expect(decoded.args).toEqual([9n]);
  });

  it("encodes ERC-20 transfer(to, amount) to the token", async () => {
    await sendErc20Transfer(CHAIN, OWNER, TOKEN, OTHER, 99n);
    const { to, decoded } = onlySent();
    expect(to).toBe(TOKEN);
    expect(decoded.functionName).toBe("transfer");
    expect(decoded.args).toEqual([OTHER, 99n]);
  });

  it("encodes ERC-20 approve(spender, exact amount) to the token", async () => {
    await sendErc20Approve(CHAIN, OWNER, TOKEN, VAULT, 123_456n);
    const { to, decoded } = onlySent();
    expect(to).toBe(TOKEN);
    expect(decoded.functionName).toBe("approve");
    expect(decoded.args).toEqual([VAULT, 123_456n]);
  });

  it("encodes IncomeDistributor.claim(id, amount, proof) to the distributor", async () => {
    await sendIncomeClaim(CHAIN, OWNER, DISTRIBUTOR, 3n, 50n, PROOF);
    const { to, decoded } = onlySent();
    expect(to).toBe(DISTRIBUTOR);
    expect(decoded.functionName).toBe("claim");
    expect(decoded.args).toEqual([3n, 50n, PROOF]);
  });
});

describe("investor transactions on the wrong chain", () => {
  const sends: Array<[string, () => Promise<Hex>]> = [
    [
      "buy",
      () =>
        sendBuy(CHAIN, OWNER, VAULT, {
          tokenAmount: 1n,
          maxQuoteAmount: 1n,
          recipient: OWNER,
          deadline: 1n,
        }),
    ],
    [
      "requestRedemption",
      () =>
        sendRequestRedemption(CHAIN, OWNER, ESCROW, {
          rwaAmount: 1n,
          minQuoteOut: 1n,
          deadline: 1n,
        }),
    ],
    [
      "requestPurchase",
      () =>
        sendRequestPurchase(CHAIN, OWNER, VAULT, {
          tokenAmount: 1n,
          maxQuoteAmount: 1n,
          recipient: OWNER,
          deadline: 1n,
        }),
    ],
    ["settlePurchase", () => sendSettlePurchase(CHAIN, OWNER, VAULT, 1n)],
    ["cancelPurchase", () => sendCancelPurchase(CHAIN, OWNER, VAULT, 1n)],
    ["claimRedemption", () => sendClaimRedemption(CHAIN, OWNER, ESCROW, 1n)],
    ["cancelRedemption", () => sendCancelRedemption(CHAIN, OWNER, ESCROW, 1n)],
    ["transfer", () => sendErc20Transfer(CHAIN, OWNER, TOKEN, OTHER, 1n)],
    ["approve", () => sendErc20Approve(CHAIN, OWNER, TOKEN, VAULT, 1n)],
    [
      "income claim",
      () => sendIncomeClaim(CHAIN, OWNER, DISTRIBUTOR, 1n, 1n, PROOF),
    ],
  ];

  it.each(sends)(
    "%s rejects with viem's ChainMismatchError and sends nothing when the wallet is on another chain",
    async (_name, send) => {
      wallet.emitChainChanged(1);
      await expect(send()).rejects.toMatchObject({
        cause: { name: "ChainMismatchError" },
      });
      expect(wallet.sentTransactions).toEqual([]);
      // The other chain's contracts are never consulted
      expect(wallet.simulatedCalls).toEqual([]);
    },
  );
});

// Simulation reverts must stop writes before the wallet prompt
describe("investor transactions simulated before sending", () => {
  const errors = parseAbi([
    "error RecipientNotAllowed(address to)",
    "error QuoteBelowMinimum(uint256 quoted, uint256 minimum)",
  ]);
  const buy = () =>
    sendBuy(CHAIN, OWNER, VAULT, {
      tokenAmount: 5n,
      maxQuoteAmount: 7n,
      recipient: OTHER,
      deadline: 1_900_000_000n,
    });
  const revertWith = (data: Hex, nested = false) => {
    wallet.uninstall();
    const revert = { code: 3, message: "execution reverted", data };
    wallet = installFakeWallet({
      account: OWNER,
      chainId: CHAIN,
      callRevert: nested
        ? { code: -32603, message: "Internal JSON-RPC error.", data: revert }
        : revert,
    });
  };

  it("simulates the exact transaction from the investor before sending it", async () => {
    await buy();
    expect(wallet.simulatedCalls).toHaveLength(1);
    const call = wallet.simulatedCalls[0];
    const sent = wallet.sentTransactions[0];
    expect({
      from: call.from?.toLowerCase(),
      to: call.to?.toLowerCase(),
      data: call.data,
    }).toEqual({
      from: sent.from?.toLowerCase(),
      to: sent.to?.toLowerCase(),
      data: sent.data,
    });
    const methods = wallet.request.mock.calls.map(([r]) => r.method);
    expect(methods.indexOf("eth_call")).toBeLessThan(
      methods.indexOf("eth_sendTransaction"),
    );
  });

  it.each([
    ["a flat", false],
    ["a MetaMask-style nested", true],
  ])(
    "refuses before the wallet prompt and names the error from %s revert",
    async (_shape, nested) => {
      revertWith(
        encodeErrorResult({
          abi: errors,
          errorName: "QuoteBelowMinimum",
          args: [5n, 10n],
        }),
        nested,
      );
      const err = await buy().catch((e: unknown) => e);
      expect(err).toBeInstanceOf(Error);
      expect((err as Error).message).toBe(
        "Transaction simulation failed: QuoteBelowMinimum(5, 10). Nothing was sent.",
      );
      expect(wallet.sentTransactions).toEqual([]);
      // The page's error text reads the revert through the cause chain
      expect(describeTxError(err, "fallback")).toBe(
        "This purchase is below the project minimum: it quotes 5 and the minimum is 10 (quote-token minimal units).",
      );
    },
  );

  it("reports an unknown revert by its selector and sends nothing", async () => {
    revertWith("0xdeadbeef");
    await expect(
      sendErc20Transfer(CHAIN, OWNER, TOKEN, OTHER, 1n),
    ).rejects.toThrow(
      "Transaction simulation failed: 0xdeadbeef. Nothing was sent.",
    );
    expect(wallet.sentTransactions).toEqual([]);
  });

  it("gives transfer refusals the compliance wording", async () => {
    revertWith(
      encodeErrorResult({
        abi: errors,
        errorName: "RecipientNotAllowed",
        args: [OTHER],
      }),
    );
    const err = await sendErc20Transfer(CHAIN, OWNER, TOKEN, OTHER, 1n).catch(
      (e: unknown) => e,
    );
    expect(describeTxError(err, "fallback")).toBe(
      "The destination wallet is not Allowed in the compliance registry.",
    );
    expect(wallet.sentTransactions).toEqual([]);
  });
});

describe("income claim with a malformed server proof", () => {
  it("rejects without sending when a proof element is not 32 bytes", async () => {
    await expect(
      sendIncomeClaim(CHAIN, OWNER, DISTRIBUTOR, 1n, 1n, ["0x1234"]),
    ).rejects.toThrow();
    expect(wallet.sentTransactions).toEqual([]);
  });
});

// Independent of src/lib/abis.ts: the revert shapes a wallet reports for the forward-order calls
const forwardErrors = parseAbi([
  "error ForwardPriced()",
  "error NotForwardPriced()",
  "error ProjectPaused()",
  "error NotPending(uint256 id)",
  "error NotBuyer(address caller, address buyer)",
  "error BuyerNotAllowed(address account)",
  "error TimeoutNotReached(uint64 availableAt, uint256 nowTs)",
  "error RoundAvailable(uint256 id, uint80 roundId)",
  "error RoundNotAvailable(uint80 roundId)",
  "error InvalidPrice(int256 answer, uint256 updatedAt)",
]);

describe("forward order revert wording", () => {
  const text = (name: string, args: unknown[] = [], context?: "income") =>
    describeTxError(
      {
        data: encodeErrorResult({
          abi: forwardErrors,
          errorName: name,
          args,
        } as never),
      },
      "fallback",
      context,
    );

  it("tells a buyer on a forward project that a direct buy is not possible", () => {
    expect(text("ForwardPriced")).toMatch(/place an order instead/);
  });

  it("tells an order placer on an instant project to buy directly", () => {
    expect(text("NotForwardPriced")).toMatch(/buy directly instead/);
  });

  it("says only the placing wallet can cancel", () => {
    expect(text("NotBuyer", [OWNER, OTHER])).toMatch(
      /Only the wallet that placed this order can cancel/,
    );
  });

  it("explains a buyer who is not Allowed instead of showing a raw revert", () => {
    const message = text("BuyerNotAllowed", [OWNER]);
    expect(message).toMatch(/buyer wallet is not Allowed/);
    expect(message).toMatch(/cannot be settled or cancelled/);
  });

  it("names the time cancel opens", () => {
    const opensAt = new Date(1_900_000_000 * 1000).toLocaleString();
    expect(
      text("TimeoutNotReached", [1_900_000_000n, 1_800_000_000n]),
    ).toContain(opensAt);
  });

  it("points to Settle when a round is available", () => {
    expect(text("RoundAvailable", [3n, 7n])).toMatch(/Settle it instead/);
  });

  it("says an order is no longer pending", () => {
    expect(text("NotPending", [3n])).toMatch(/no longer pending/);
  });

  it("says settlement waits for an unpause, without claim wording", () => {
    const message = text("ProjectPaused");
    expect(message).toMatch(/settlement resumes/);
    expect(message).not.toMatch(/claims/);
  });

  it("keeps the claim wording for ProjectPaused on an income claim", () => {
    expect(text("ProjectPaused", [], "income")).toMatch(/claims resume/);
  });

  it("says no price has been published when settling before the round lands", () => {
    expect(text("RoundNotAvailable", [8n])).toMatch(
      /No price has been published/,
    );
  });
});

describe("readOrderRound", () => {
  const STRATEGY = "0x7777777777777777777777777777777777777777" as Address;
  const args = {
    tokenAmount: 5_000_000n,
    baseRound: 100n,
    createdAt: 1_900_000_000n,
  };
  const withWallet = (opts: Parameters<typeof installFakeWallet>[0]) => {
    wallet.uninstall();
    wallet = installFakeWallet({ account: OWNER, chainId: CHAIN, ...opts });
  };
  const revert = (data: Hex) => ({
    code: 3,
    message: "execution reverted",
    data,
  });

  it("returns the quote and round of a priced order and reads the order's own strategy", async () => {
    withWallet({
      callResult: encodeFunctionResult({
        abi,
        functionName: "quotePurchaseAfter",
        result: [4_950_000n, 123n],
      }),
    });
    await expect(readOrderRound(CHAIN, STRATEGY, args)).resolves.toEqual({
      state: "priced",
      quote: 4_950_000n,
      roundId: 123n,
    });
    const call = wallet.simulatedCalls[0];
    expect(call.to?.toLowerCase()).toBe(STRATEGY);
    const decoded = decodeFunctionData({ abi, data: call.data as Hex });
    expect(decoded.functionName).toBe("quotePurchaseAfter");
    expect(decoded.args).toEqual([5_000_000n, 100n, 1_900_000_000n]);
  });

  it("reads RoundNotAvailable as waiting", async () => {
    withWallet({
      callRevert: revert(
        encodeErrorResult({
          abi: forwardErrors,
          errorName: "RoundNotAvailable",
          args: [101n],
        }),
      ),
    });
    await expect(readOrderRound(CHAIN, STRATEGY, args)).resolves.toEqual({
      state: "waiting",
    });
  });

  it("reads a MetaMask-style nested RoundNotAvailable as waiting", async () => {
    withWallet({
      callRevert: {
        code: -32603,
        message: "Internal JSON-RPC error.",
        data: revert(
          encodeErrorResult({
            abi: forwardErrors,
            errorName: "RoundNotAvailable",
            args: [101n],
          }),
        ),
      },
    });
    await expect(readOrderRound(CHAIN, STRATEGY, args)).resolves.toEqual({
      state: "waiting",
    });
  });

  it("reads an InvalidPrice revert as an unusable round", async () => {
    withWallet({
      callRevert: revert(
        encodeErrorResult({
          abi: forwardErrors,
          errorName: "InvalidPrice",
          args: [0n, 5n],
        }),
      ),
    });
    await expect(readOrderRound(CHAIN, STRATEGY, args)).resolves.toEqual({
      state: "unusable",
      reason: "the price feed returned an invalid price",
    });
  });

  it("reads a revert it does not recognize as unusable, not as waiting", async () => {
    withWallet({ callRevert: revert("0xdeadbeef") });
    await expect(readOrderRound(CHAIN, STRATEGY, args)).resolves.toMatchObject({
      state: "unusable",
    });
  });

  it("rejects instead of guessing when the read fails without revert data", async () => {
    withWallet({ callRevert: new Error("network down") });
    await expect(readOrderRound(CHAIN, STRATEGY, args)).rejects.toThrow();
  });

  it("rejects without consulting the contract when the wallet is on another chain", async () => {
    wallet.emitChainChanged(1);
    await expect(readOrderRound(CHAIN, STRATEGY, args)).rejects.toMatchObject({
      name: "ChainMismatchError",
    });
    expect(wallet.simulatedCalls).toEqual([]);
  });
});
