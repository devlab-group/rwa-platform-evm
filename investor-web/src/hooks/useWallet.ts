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
  // True when wallet and project chain IDs are known and differ
  chainMismatch: boolean;
  // Requests wallet_switchEthereumChain for expectedChainId
  switchChain: () => Promise<void>;
}

/**
 * Adds project-chain mismatch and bound switching to WalletProvider state; expectedChainId
 * is undefined until loaded. lib/wallet.ts rechecks live chain before reads/sends.
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
