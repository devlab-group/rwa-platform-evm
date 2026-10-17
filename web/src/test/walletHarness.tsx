// Test-only render/install helpers intentionally export beyond components
/* eslint-disable react-refresh/only-export-components */
import { render, type RenderResult } from "@testing-library/react";
import { useEffect, type ReactElement } from "react";
import { hexToString, type Hex } from "viem";
import { vi } from "vitest";
import { WalletProvider } from "../context/WalletContext";
import { useWalletContext } from "../context/walletContextValue";
import type { InjectedProvider } from "../lib/wallet";

export interface FakeWalletOptions {
  account?: string;
  chainId?: number;
  /** Signature personal_sign returns; defaults to a 65-byte hex string */
  signature?: string;
  /** When set, personal_sign rejects with this, for example a user-rejection error */
  signRejection?: unknown;
  /** Unknown chains reject switching with 4902 until added, exercising the add-chain fallback */
  notAddedChains?: number[];
}

const DEFAULT_ACCOUNT = "0x1111111111111111111111111111111111111111";
const DEFAULT_SIGNATURE = `0x${"ab".repeat(65)}`;

/**
 * EIP-1193 fake for real viem clients; exposes request spy and account/chain events for
 * WalletProvider tests
 */
export function installFakeWallet(opts: FakeWalletOptions = {}) {
  const account = opts.account ?? DEFAULT_ACCOUNT;
  const initialChainId = opts.chainId ?? 31337;
  const signature = opts.signature ?? DEFAULT_SIGNATURE;
  // personal_sign requests, decoded: the message text and the account asked to sign
  const signedMessages: Array<{ message: string; signer: string }> = [];
  const listeners = new Map<string, Array<(...args: unknown[]) => void>>();

  let currentChainId = initialChainId;
  const notAdded = new Set(opts.notAddedChains ?? []);

  const emit = (event: string, payload: unknown) => {
    for (const h of listeners.get(event) ?? []) h(payload);
  };

  const request = vi.fn(
    async ({ method, params }: { method: string; params?: unknown[] }) => {
      switch (method) {
        case "eth_accounts":
        case "eth_requestAccounts":
          return [account];
        case "eth_chainId":
          return `0x${currentChainId.toString(16)}`;
        case "personal_sign":
          if (opts.signRejection) throw opts.signRejection;
          signedMessages.push({
            message: hexToString(params?.[0] as Hex),
            signer: params?.[1] as string,
          });
          return signature;
        case "wallet_switchEthereumChain": {
          const target = Number((params?.[0] as { chainId: string }).chainId);
          if (notAdded.has(target)) {
            const err = new Error(
              "Unrecognized chain ID. Try adding the chain first.",
            ) as Error & { code: number };
            err.code = 4902;
            throw err;
          }
          currentChainId = target;
          emit("chainChanged", `0x${target.toString(16)}`);
          return null;
        }
        case "wallet_addEthereumChain": {
          const target = Number((params?.[0] as { chainId: string }).chainId);
          notAdded.delete(target); // Chain is now known
          return null;
        }
        default:
          throw new Error(`unexpected method ${method}`);
      }
    },
  );

  const provider: InjectedProvider = {
    request: request as InjectedProvider["request"],
    on: (event, handler) => {
      const arr = listeners.get(event) ?? [];
      arr.push(handler);
      listeners.set(event, arr);
    },
    removeListener: (event, handler) => {
      const arr = listeners.get(event);
      if (arr)
        listeners.set(
          event,
          arr.filter((h) => h !== handler),
        );
    },
  };

  window.ethereum = provider;

  return {
    account,
    chainId: initialChainId,
    signature,
    signedMessages,
    request,
    /** Fires accountsChanged to every registered handler */
    emitAccountsChanged(accounts: string[]) {
      emit("accountsChanged", accounts);
    },
    // Simulates a network change made in the wallet
    emitChainChanged(chainId: number) {
      currentChainId = chainId;
      emit("chainChanged", `0x${chainId.toString(16)}`);
    },
    uninstall() {
      delete window.ethereum;
    },
  };
}

// Connects once on mount for tests; renders nothing
function ConnectOnMount() {
  const { connect } = useWalletContext();
  useEffect(() => {
    void connect();
  }, [connect]);
  return null;
}

/**
 * Wraps ui in WalletProvider; connected: true auto-connects the fake installed by
 * installFakeWallet()
 */
export function renderWithWallet(
  ui: ReactElement,
  { connected = false }: { connected?: boolean } = {},
): RenderResult {
  return render(
    <WalletProvider>
      {connected && <ConnectOnMount />}
      {ui}
    </WalletProvider>,
  );
}
