// Injects window.ethereum before page scripts for wallet flows without an
// extension. Node encodes fixed balance and decimals responses with viem and
// passes plain data; the browser pads argument-dependent allowance and quote
// responses as uint256 words without importing dependencies.
import type { Page } from "@playwright/test";
import {
  encodeErrorResult,
  encodeFunctionResult,
  erc20Abi,
  parseAbi,
  toFunctionSelector,
} from "viem";

export const MOCK_WALLET_ADDRESS =
  "0x1111111111111111111111111111111111111111" as const;
export const MOCK_CHAIN_ID = 31337;
export const MOCK_TOKEN_DECIMALS = 18;
export const MOCK_TOKEN_BALANCE = 1_500_000_000_000_000_000n; // 1.5 tokens
/** Quote-token price of one whole RWA token, as the mock Vault and escrow views report it. */
export const MOCK_PURCHASE_PRICE = 1_000_000n;
export const MOCK_REDEMPTION_PRICE = 950_000n;
/** Price per whole token of the round that prices a forward order once the mock feed has published it */
export const MOCK_ROUND_PRICE = 990_000n;
export const MOCK_ROUND_ID = 102n;

const BALANCE_OF_SELECTOR = "0x70a08231";
const DECIMALS_SELECTOR = "0x313ce567";
const APPROVE_SELECTOR = "0x095ea7b3";
const ALLOWANCE_SELECTOR = "0xdd62ed3e";
const PREVIEW_BUY_SELECTOR = "0x48153279";
const PREVIEW_REDEEM_SELECTOR = "0x4cdad506";
const QUOTE_PURCHASE_AFTER_SELECTOR = toFunctionSelector(
  "quotePurchaseAfter(uint256,uint80,uint64)",
);
const GET_PURCHASE_SELECTOR = toFunctionSelector("getPurchase(uint256)");
const STRATEGY_SELECTOR = toFunctionSelector("strategy()");
const FORWARD_PRICED_SELECTOR = toFunctionSelector("forwardPriced()");
/** Strategy address the mock Vault names; matches the mock API's project strategy */
const MOCK_STRATEGY = `0x${"77".repeat(20)}`;

export interface InstallMockWalletOptions {
  /** Chain the mock wallet reports via eth_chainId (default MOCK_CHAIN_ID). */
  chainId?: number;
}

export async function installMockWallet(
  page: Page,
  options: InstallMockWalletOptions = {},
): Promise<void> {
  const balanceOfResult = encodeFunctionResult({
    abi: erc20Abi,
    functionName: "balanceOf",
    result: MOCK_TOKEN_BALANCE,
  });
  const decimalsResult = encodeFunctionResult({
    abi: erc20Abi,
    functionName: "decimals",
    result: MOCK_TOKEN_DECIMALS,
  });

  // quotePurchaseAfter reverts with this until a spec calls setMockRound(page, true)
  const roundNotAvailable = encodeErrorResult({
    abi: parseAbi(["error RoundNotAvailable(uint80 roundId)"]),
    errorName: "RoundNotAvailable",
    args: [MOCK_ROUND_ID - 1n],
  });

  await page.addInitScript(
    (args: {
      address: string;
      chainIdHex: string;
      balanceOfResult: string;
      decimalsResult: string;
      balanceOfSelector: string;
      decimalsSelector: string;
      approveSelector: string;
      allowanceSelector: string;
      previewBuySelector: string;
      previewRedeemSelector: string;
      quotePurchaseAfterSelector: string;
      getPurchaseSelector: string;
      strategySelector: string;
      forwardPricedSelector: string;
      strategy: string;
      roundNotAvailable: string;
      roundPrice: string;
      roundId: string;
      purchasePrice: string;
      redemptionPrice: string;
    }) => {
      let txNonce = 0;
      let blockNumber = 1000;
      // Approved amount by `token:spender`, so a later allowance() read reflects
      // an earlier approve() send; the Buy flow approves, waits for the receipt,
      // then reads the allowance before enabling Buy
      const allowances = new Map<string, bigint>();
      const receipts = new Map<
        string,
        { blockNumber: number; to: string | null }
      >();
      const sentTransactions: { to: string | null; data: string | null }[] = [];
      let currentChainIdHex = args.chainIdHex;
      // Whether the feed has published the round that prices a forward order
      let roundPriced = false;

      const provider = {
        isMockWallet: true,
        _listeners: new Map<string, Set<(...a: unknown[]) => void>>(),
        on(event: string, handler: (...a: unknown[]) => void) {
          if (!this._listeners.has(event))
            this._listeners.set(event, new Set());
          this._listeners.get(event)!.add(handler);
        },
        removeListener(event: string, handler: (...a: unknown[]) => void) {
          this._listeners.get(event)?.delete(handler);
        },
        _emit(event: string, payload: unknown) {
          for (const handler of this._listeners.get(event) ?? [])
            handler(payload);
        },
        // Test-only hook, not part of EIP-1193: specs call it through
        // page.evaluate to simulate the wallet switching networks
        __setChainId(chainId: number) {
          currentChainIdHex = `0x${chainId.toString(16)}`;
          this._emit("chainChanged", currentChainIdHex);
        },
        // Test-only hook: publishes (or withdraws) the round that prices forward orders
        __setRoundPriced(priced: boolean) {
          roundPriced = priced;
        },
        // Test-only hook: exposes every eth_sendTransaction payload so specs can
        // decode the outgoing calldata (e.g. an approval's token and spender)
        __getSentTransactions() {
          return sentTransactions;
        },
        async request({
          method,
          params,
        }: {
          method: string;
          params?: unknown[];
        }) {
          switch (method) {
            case "eth_requestAccounts":
            case "eth_accounts":
              return [args.address];
            case "eth_chainId":
              return currentChainIdHex;
            case "wallet_switchEthereumChain": {
              const requested = (params?.[0] as { chainId?: string })?.chainId;
              if (requested) {
                currentChainIdHex = requested;
                this._emit("chainChanged", currentChainIdHex);
              }
              return null;
            }
            case "personal_sign":
            case "eth_sign":
              return `0x${"11".repeat(65)}`;
            case "eth_sendTransaction": {
              txNonce += 1;
              const txHash = `0x${txNonce.toString(16).padStart(64, "0")}`;
              const tx = (params?.[0] ?? {}) as { data?: string; to?: string };
              const data = tx.data ?? "";
              sentTransactions.push({ to: tx.to ?? null, data: data || null });
              if (data.startsWith(args.approveSelector) && tx.to) {
                const spender =
                  `0x${data.slice(10, 74).slice(24)}`.toLowerCase();
                const amount = BigInt(`0x${data.slice(74, 138)}`);
                allowances.set(`${tx.to.toLowerCase()}:${spender}`, amount);
              }
              blockNumber += 1;
              receipts.set(txHash, { blockNumber, to: tx.to ?? null });
              return txHash;
            }
            case "eth_getTransactionReceipt": {
              const hash = params?.[0] as string;
              const receipt = receipts.get(hash);
              if (!receipt) return null;
              return {
                transactionHash: hash,
                status: "0x1",
                blockNumber: `0x${receipt.blockNumber.toString(16)}`,
                blockHash: `0x${"aa".repeat(32)}`,
                transactionIndex: "0x0",
                from: args.address,
                to: receipt.to,
                contractAddress: null,
                cumulativeGasUsed: "0x5208",
                gasUsed: "0x5208",
                effectiveGasPrice: "0x3b9aca00",
                logsBloom: `0x${"0".repeat(512)}`,
                logs: [],
                type: "0x2",
              };
            }
            case "eth_blockNumber":
              return `0x${(blockNumber + 1).toString(16)}`;
            case "eth_call": {
              const call = (params?.[0] ?? {}) as {
                data?: string;
                to?: string;
              };
              const data = call.data ?? "";
              if (data.startsWith(args.balanceOfSelector))
                return args.balanceOfResult;
              if (data.startsWith(args.decimalsSelector))
                return args.decimalsResult;
              if (data.startsWith(args.allowanceSelector)) {
                const spender =
                  `0x${data.slice(74, 138).slice(24)}`.toLowerCase();
                const token = (call.to ?? "").toLowerCase();
                const amount = allowances.get(`${token}:${spender}`) ?? 0n;
                return `0x${amount.toString(16).padStart(64, "0")}`;
              }
              // Vault.strategy() and strategy.forwardPriced(): the chain's own answer on
              // whether purchases are orders or instant buys, set by the specs through
              // window.__mockForwardPriced; window.__mockModeUnreadable makes the Vault
              // return no strategy, as a failing RPC read does
              const mode = window as unknown as {
                __mockForwardPriced?: boolean;
                __mockModeUnreadable?: boolean;
              };
              if (data.startsWith(args.strategySelector)) {
                if (mode.__mockModeUnreadable) return "0x";
                return `0x${args.strategy.slice(2).padStart(64, "0")}`;
              }
              if (data.startsWith(args.forwardPricedSelector)) {
                return `0x${(mode.__mockForwardPriced ? 1 : 0)
                  .toString(16)
                  .padStart(64, "0")}`;
              }
              // Vault.getPurchase(id): the order struct, whose last word is the minimum the
              // Vault recorded on the order (window.__mockOrderMinQuote, default none)
              if (data.startsWith(args.getPurchaseSelector)) {
                const recorded = BigInt(
                  (window as unknown as { __mockOrderMinQuote?: string })
                    .__mockOrderMinQuote ?? "0",
                );
                const word = (n: bigint) => n.toString(16).padStart(64, "0");
                return `0x${word(0n).repeat(8)}${word(recorded)}`;
              }
              // quotePurchaseAfter(tokenAmount, baseRound, orderTime) on the order's
              // strategy: the quote of the pricing round, or RoundNotAvailable until the
              // feed has published it
              if (data.startsWith(args.quotePurchaseAfterSelector)) {
                if (!roundPriced) {
                  throw Object.assign(new Error("execution reverted"), {
                    code: 3,
                    data: args.roundNotAvailable,
                  });
                }
                const amount = BigInt(`0x${data.slice(10, 74)}`);
                const quote = (amount * BigInt(args.roundPrice)) / 10n ** 18n;
                const word = (n: bigint) => n.toString(16).padStart(64, "0");
                return `0x${word(quote)}${word(BigInt(args.roundId))}`;
              }
              // Vault.previewBuy and RedemptionEscrow.previewRedeem, the buy and
              // redemption quotes. Both take a uint256 amount in RWA minimal
              // units and return a quote-token amount at the fixed price
              const isPreviewBuy = data.startsWith(args.previewBuySelector);
              if (isPreviewBuy || data.startsWith(args.previewRedeemSelector)) {
                const amount = BigInt(`0x${data.slice(10, 74)}`);
                const price = BigInt(
                  isPreviewBuy ? args.purchasePrice : args.redemptionPrice,
                );
                const quote = (amount * price) / 10n ** 18n;
                return `0x${quote.toString(16).padStart(64, "0")}`;
              }
              return "0x";
            }
            case "eth_estimateGas":
              return "0x5208";
            case "eth_gasPrice":
              return "0x3b9aca00";
            default:
              return null;
          }
        },
      };
      (window as unknown as { ethereum: unknown }).ethereum = provider;
    },
    {
      address: MOCK_WALLET_ADDRESS,
      chainIdHex: `0x${(options.chainId ?? MOCK_CHAIN_ID).toString(16)}`,
      balanceOfResult,
      decimalsResult,
      balanceOfSelector: BALANCE_OF_SELECTOR,
      decimalsSelector: DECIMALS_SELECTOR,
      approveSelector: APPROVE_SELECTOR,
      allowanceSelector: ALLOWANCE_SELECTOR,
      previewBuySelector: PREVIEW_BUY_SELECTOR,
      previewRedeemSelector: PREVIEW_REDEEM_SELECTOR,
      quotePurchaseAfterSelector: QUOTE_PURCHASE_AFTER_SELECTOR,
      getPurchaseSelector: GET_PURCHASE_SELECTOR,
      strategySelector: STRATEGY_SELECTOR,
      forwardPricedSelector: FORWARD_PRICED_SELECTOR,
      strategy: MOCK_STRATEGY,
      roundNotAvailable,
      roundPrice: MOCK_ROUND_PRICE.toString(),
      roundId: MOCK_ROUND_ID.toString(),
      purchasePrice: MOCK_PURCHASE_PRICE.toString(),
      redemptionPrice: MOCK_REDEMPTION_PRICE.toString(),
    },
  );
}

/** Simulates the wallet switching networks after connect, for the chain guard specs. */
export async function mockWalletSwitchChain(
  page: Page,
  chainId: number,
): Promise<void> {
  await page.evaluate((id: number) => {
    (
      window as unknown as { ethereum: { __setChainId: (c: number) => void } }
    ).ethereum.__setChainId(id);
  }, chainId);
}

/** Publishes the feed round that prices forward orders; before this, reading an order's price reverts */
export async function setMockRound(page: Page, priced: boolean): Promise<void> {
  await page.evaluate((value: boolean) => {
    (
      window as unknown as {
        ethereum: { __setRoundPriced: (p: boolean) => void };
      }
    ).ethereum.__setRoundPriced(value);
  }, priced);
}

export interface MockSentTransaction {
  to: string | null;
  data: string | null;
}

/** Returns every `eth_sendTransaction` payload submitted so far, for calldata-decode assertions. */
export async function getMockSentTransactions(
  page: Page,
): Promise<MockSentTransaction[]> {
  return page.evaluate(() =>
    (
      window as unknown as {
        ethereum: { __getSentTransactions: () => MockSentTransaction[] };
      }
    ).ethereum.__getSentTransactions(),
  );
}

/** Makes the mock chain report forward pricing (or instant pricing) for the Vault's strategy; call before goto */
export async function setMockForwardPriced(
  page: Page,
  forward: boolean,
): Promise<void> {
  await page.addInitScript((value: boolean) => {
    (
      window as unknown as { __mockForwardPriced: boolean }
    ).__mockForwardPriced = value;
  }, forward);
}

/** Makes the mock Vault return no strategy, so the pricing mode cannot be read; call before goto */
export async function setMockModeUnreadable(page: Page): Promise<void> {
  await page.addInitScript(() => {
    (
      window as unknown as { __mockModeUnreadable: boolean }
    ).__mockModeUnreadable = true;
  });
}

/** Sets the minimum the mock Vault recorded on every order, in quote-token minimal units; call before goto */
export async function setMockOrderMinQuote(
  page: Page,
  minimum: bigint,
): Promise<void> {
  await page.addInitScript((value: string) => {
    (window as unknown as { __mockOrderMinQuote: string }).__mockOrderMinQuote =
      value;
  }, minimum.toString());
}
