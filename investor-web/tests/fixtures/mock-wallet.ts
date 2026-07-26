// Injects a minimal EIP-1193 provider as window.ethereum before any page
// script runs, so the Buy/Redemption/Transfer/Wallet flows (src/lib/wallet.ts)
// work without a real browser extension. Return values for eth_call are
// pre-encoded here (in Node, where viem's full encoder is available) and
// handed to the injected script as plain data — the in-page script itself
// stays framework-free. The argument-dependent ones (allowance, the two price
// previews) are single uint256 words, so the in-page script pads them itself.
import type { Page } from "@playwright/test";
import { encodeFunctionResult, erc20Abi } from "viem";

export const MOCK_WALLET_ADDRESS = "0x1111111111111111111111111111111111111111" as const;
export const MOCK_CHAIN_ID = 31337;
export const MOCK_TOKEN_DECIMALS = 18;
export const MOCK_TOKEN_BALANCE = 1_500_000_000_000_000_000n; // 1.5 tokens
/** Quote-token price of one whole RWA token, as the mock Vault/escrow views report it. */
export const MOCK_PURCHASE_PRICE = 1_000_000n;
export const MOCK_REDEMPTION_PRICE = 950_000n;

const BALANCE_OF_SELECTOR = "0x70a08231";
const DECIMALS_SELECTOR = "0x313ce567";
const APPROVE_SELECTOR = "0x095ea7b3";
const ALLOWANCE_SELECTOR = "0xdd62ed3e";
const PREVIEW_BUY_SELECTOR = "0x48153279";
const PREVIEW_REDEEM_SELECTOR = "0x4cdad506";

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
      purchasePrice: string;
      redemptionPrice: string;
    }) => {
      let txNonce = 0;
      let blockNumber = 1000;
      // token address -> spender -> approved amount, so a later allowance()
      // read reflects a preceding approve() send — the Buy flow does
      // approve → confirm receipt → read allowance before enabling Buy.
      const allowances = new Map<string, bigint>();
      const receipts = new Map<string, { blockNumber: number; to: string | null }>();
      const sentTransactions: { to: string | null; data: string | null }[] = [];
      let currentChainIdHex = args.chainIdHex;

      const provider = {
        isMockWallet: true,
        _listeners: new Map<string, Set<(...a: unknown[]) => void>>(),
        on(event: string, handler: (...a: unknown[]) => void) {
          if (!this._listeners.has(event)) this._listeners.set(event, new Set());
          this._listeners.get(event)!.add(handler);
        },
        removeListener(event: string, handler: (...a: unknown[]) => void) {
          this._listeners.get(event)?.delete(handler);
        },
        _emit(event: string, payload: unknown) {
          for (const handler of this._listeners.get(event) ?? []) handler(payload);
        },
        // Test-only hook (not part of EIP-1193): specs call this directly via
        // page.evaluate to simulate the wallet switching networks.
        __setChainId(chainId: number) {
          currentChainIdHex = `0x${chainId.toString(16)}`;
          this._emit("chainChanged", currentChainIdHex);
        },
        // Test-only hook: exposes every eth_sendTransaction payload so specs
        // can decode and assert on the outgoing calldata (e.g. an approval's
        // token + spender).
        __getSentTransactions() {
          return sentTransactions;
        },
        async request({ method, params }: { method: string; params?: unknown[] }) {
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
                const spender = `0x${data.slice(10, 74).slice(24)}`.toLowerCase();
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
              const call = (params?.[0] ?? {}) as { data?: string; to?: string };
              const data = call.data ?? "";
              if (data.startsWith(args.balanceOfSelector)) return args.balanceOfResult;
              if (data.startsWith(args.decimalsSelector)) return args.decimalsResult;
              if (data.startsWith(args.allowanceSelector)) {
                const spender = `0x${data.slice(74, 138).slice(24)}`.toLowerCase();
                const token = (call.to ?? "").toLowerCase();
                const amount = allowances.get(`${token}:${spender}`) ?? 0n;
                return `0x${amount.toString(16).padStart(64, "0")}`;
              }
              // Vault.previewBuy / RedemptionEscrow.previewRedeem — the buy and
              // redemption quotes, now read from the chain rather than the API.
              // Both take a single uint256 amount in RWA minimal units and
              // return a quote-token amount at the fixed price.
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
      purchasePrice: MOCK_PURCHASE_PRICE.toString(),
      redemptionPrice: MOCK_REDEMPTION_PRICE.toString(),
    },
  );
}

/** Simulates the wallet switching networks after connect, for the chain-binding tests. */
export async function mockWalletSwitchChain(page: Page, chainId: number): Promise<void> {
  await page.evaluate((id: number) => {
    (window as unknown as { ethereum: { __setChainId: (c: number) => void } }).ethereum.__setChainId(id);
  }, chainId);
}

export interface MockSentTransaction {
  to: string | null;
  data: string | null;
}

/** Returns every `eth_sendTransaction` payload submitted so far, for calldata-decode assertions. */
export async function getMockSentTransactions(page: Page): Promise<MockSentTransaction[]> {
  return page.evaluate(() =>
    (
      window as unknown as { ethereum: { __getSentTransactions: () => MockSentTransaction[] } }
    ).ethereum.__getSentTransactions(),
  );
}
