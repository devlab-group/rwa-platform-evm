import { createContext, useContext } from "react";
import type { Address, Hex } from "viem";

export type WalletStatus = "disconnected" | "connecting" | "connected";

/**
 * Shared wallet state for WalletContext.tsx; separate exports keep its provider compatible with
 * react-refresh
 */
export interface WalletContextValue {
  address: Address | null;
  chainId: number | null;
  status: WalletStatus;
  connecting: boolean;
  switching: boolean;
  error: string | null;
  /** Requests account access through eth_requestAccounts */
  connect: () => Promise<void>;
  /** Forgets the account locally without revoking the wallet's site permission */
  disconnect: () => void;
  /** personal_sign with the connected account; throws when disconnected */
  signMessage: (message: string) => Promise<Hex>;
  /** Requests wallet_switchEthereumChain, then refreshes chainId */
  switchChain: (expectedChainId: number) => Promise<void>;
}

export const WalletContext = createContext<WalletContextValue | undefined>(
  undefined,
);

/** Reads shared wallet state; throws outside WalletProvider */
export function useWalletContext(): WalletContextValue {
  const ctx = useContext(WalletContext);
  if (!ctx)
    throw new Error("useWalletContext must be used within a <WalletProvider>.");
  return ctx;
}
