import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  decodeFunctionData,
  encodeErrorResult,
  parseAbi,
  type Address,
  type Hex,
} from "viem";
import { installFakeWallet } from "../test/walletHarness";
import {
  describeTxError,
  sendBuy,
  sendCancelRedemption,
  sendClaimRedemption,
  sendErc20Approve,
  sendErc20Transfer,
  sendIncomeClaim,
  sendRequestRedemption,
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
