// Test-only helper (never in the app bundle), so react-refresh's
// "only export components" constraint — which exists purely for dev HMR —
// doesn't apply here; it intentionally exports render/install helpers too.
/* eslint-disable react-refresh/only-export-components */
import { render, type RenderResult } from "@testing-library/react";
import { useEffect, type ReactElement } from "react";
import { vi } from "vitest";
import { WalletProvider } from "../context/WalletContext";
import { useWalletContext } from "../context/walletContextValue";
import type { InjectedProvider } from "../lib/wallet";

export interface FakeWalletOptions {
  account?: string;
  chainId?: number;
  /** Fixed signature returned by personal_sign; defaults to a valid-length 65-byte hex. */
  signature?: string;
  /** When set, personal_sign rejects with this (e.g. a user-rejection error). */
  signRejection?: unknown;
  /**
   * Chains the wallet doesn't know yet: wallet_switchEthereumChain to one of
   * these rejects with code 4902 until it's added via wallet_addEthereumChain
   * (mirrors MetaMask not having a local anvil chain). Exercises the add-chain
   * fallback in lib/wallet.ts.
   */
  notAddedChains?: number[];
}

const DEFAULT_ACCOUNT = "0x1111111111111111111111111111111111111111";
const DEFAULT_SIGNATURE = `0x${"ab".repeat(65)}`;

/**
 * Installs a fake EIP-1193 injected provider on `window.ethereum` that a real
 * viem client (lib/wallet.ts) can drive: it answers eth_accounts/
 * eth_requestAccounts/eth_chainId and personal_sign. Returned handle exposes
 * the request spy and lets a test flip the connected account (accountsChanged)
 * so WalletProvider's listeners can be exercised.
 */
export function installFakeWallet(opts: FakeWalletOptions = {}) {
  const account = opts.account ?? DEFAULT_ACCOUNT;
  const initialChainId = opts.chainId ?? 31337;
  const signature = opts.signature ?? DEFAULT_SIGNATURE;
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
          notAdded.delete(target); // now known to the wallet
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
    request,
    /** Fire an accountsChanged event to every registered handler. */
    emitAccountsChanged(accounts: string[]) {
      emit("accountsChanged", accounts);
    },
    /** Simulate the wallet switching networks on its own (e.g. the user picks it in MetaMask). */
    emitChainChanged(chainId: number) {
      currentChainId = chainId;
      emit("chainChanged", `0x${chainId.toString(16)}`);
    },
    uninstall() {
      delete window.ethereum;
    },
  };
}

/**
 * Fires the context's connect() once on mount so a test starts with a
 * connected wallet, which is the state every investor action assumes.
 * Renders nothing.
 */
function ConnectOnMount() {
  const { connect } = useWalletContext();
  useEffect(() => {
    void connect();
  }, [connect]);
  return null;
}

/**
 * Renders `ui` inside a real <WalletProvider> so context-reading components
 * work. Pass `{ connected: true }` to auto-connect the (fake) wallet on mount;
 * requires installFakeWallet() to have been called first.
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
