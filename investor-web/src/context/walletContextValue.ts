import { createContext, useContext } from "react";
import type { Address, Hex } from "viem";

export type WalletStatus = "disconnected" | "connecting" | "connected";

/**
 * App-wide connected-wallet state (see WalletContext.tsx for the provider).
 * Kept in its own module so WalletContext.tsx exports only a component, as the
 * react-refresh lint rule requires.
 */
export interface WalletContextValue {
  address: Address | null;
  chainId: number | null;
  status: WalletStatus;
  connecting: boolean;
  switching: boolean;
  error: string | null;
  /** Prompts the wallet for account access (eth_requestAccounts). */
  connect: () => Promise<void>;
  /** Client-side disconnect: forgets the account locally and does not revoke the wallet's permission for this site. */
  disconnect: () => void;
  /** personal_sign the given message with the connected account. Throws if no wallet is connected. */
  signMessage: (message: string) => Promise<Hex>;
  /** Requests the wallet switch to `expectedChainId` (wallet_switchEthereumChain), then refreshes chainId. */
  switchChain: (expectedChainId: number) => Promise<void>;
}

export const WalletContext = createContext<WalletContextValue | undefined>(
  undefined,
);

/** Reads the app-wide wallet context. Throws if used outside <WalletProvider>. */
export function useWalletContext(): WalletContextValue {
  const ctx = useContext(WalletContext);
  if (!ctx)
    throw new Error("useWalletContext must be used within a <WalletProvider>.");
  return ctx;
}
