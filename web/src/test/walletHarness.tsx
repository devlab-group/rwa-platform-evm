// Test-only helper, never in the app bundle. react-refresh's "only export
// components" rule exists for dev HMR and does not apply, so this file also
// exports render and install helpers.
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
  /** Signature personal_sign returns; defaults to a 65-byte hex string */
  signature?: string;
  /** When set, personal_sign rejects with this, for example a user-rejection error */
  signRejection?: unknown;
  /**
   * Chains the wallet does not know yet. wallet_switchEthereumChain to one of
   * them rejects with code 4902 until wallet_addEthereumChain adds it, which
   * exercises the add-chain fallback in lib/wallet.ts.
   */
  notAddedChains?: number[];
}

const DEFAULT_ACCOUNT = "0x1111111111111111111111111111111111111111";
const DEFAULT_SIGNATURE = `0x${"ab".repeat(65)}`;

/**
 * Installs a fake EIP-1193 provider on `window.ethereum` that a viem client
 * (lib/wallet.ts) can drive. It answers eth_accounts, eth_requestAccounts,
 * eth_chainId and personal_sign. The returned handle exposes the request spy
 * and lets a test change the account or chain to exercise WalletProvider's
 * listeners.
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
    /** Fires accountsChanged to every registered handler */
    emitAccountsChanged(accounts: string[]) {
      emit("accountsChanged", accounts);
    },
    /** Simulates the wallet switching networks without the app, as when the user picks a network in the wallet */
    emitChainChanged(chainId: number) {
      currentChainId = chainId;
      emit("chainChanged", `0x${chainId.toString(16)}`);
    },
    uninstall() {
      delete window.ethereum;
    },
  };
}

/** Calls the context's connect() once on mount so a test starts with a connected wallet. Renders nothing */
function ConnectOnMount() {
  const { connect } = useWalletContext();
  useEffect(() => {
    void connect();
  }, [connect]);
  return null;
}

/**
 * Renders `ui` inside a WalletProvider. Pass `{ connected: true }` to connect
 * the fake wallet on mount; call installFakeWallet() first.
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
