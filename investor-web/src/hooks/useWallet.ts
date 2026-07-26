import { useCallback } from "react";
import type { Address } from "viem";
import { useWalletContext } from "../context/walletContextValue";

export interface WalletState {
  address: Address | null;
  chainId: number | null;
  connecting: boolean;
  switching: boolean;
  error: string | null;
  connect: () => Promise<void>;
  /** true once both the wallet and the project's chain are known and they differ. */
  chainMismatch: boolean;
  /** Requests the wallet switch to `expectedChainId` (wallet_switchEthereumChain). */
  switchChain: () => Promise<void>;
}

/**
 * Per-page binding over the app-wide WalletProvider. The connection itself
 * (address/chainId/connect/events) lives in the context so it's shared across
 * every screen; this hook only layers on the project-specific chain-mismatch
 * check using the caller's `expectedChainId` (undefined until the project has
 * loaded), and binds `switchChain` to that chain — lib/wallet.ts
 * additionally re-asserts the chain right before each read/sign/send, since
 * this state can be stale between a subscription event and a click.
 */
export function useWallet(expectedChainId: number | undefined): WalletState {
  const wallet = useWalletContext();

  const switchChain = useCallback(async () => {
    if (!expectedChainId) return;
    await wallet.switchChain(expectedChainId);
  }, [expectedChainId, wallet]);

  const chainMismatch = Boolean(
    expectedChainId &&
    wallet.chainId !== null &&
    wallet.chainId !== expectedChainId,
  );

  return {
    address: wallet.address,
    chainId: wallet.chainId,
    connecting: wallet.connecting,
    switching: wallet.switching,
    error: wallet.error,
    connect: wallet.connect,
    chainMismatch,
    switchChain,
  };
}
