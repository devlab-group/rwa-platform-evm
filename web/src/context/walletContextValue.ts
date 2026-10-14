import { createContext, useContext } from "react";
import type { Address, Hex } from "viem";

export type WalletStatus = "disconnected" | "connecting" | "connected";

/**
 * App-wide connected-wallet state; WalletContext.tsx provides it. It lives in
 * its own module so WalletContext.tsx exports only the component.
 */
export interface WalletContextValue {
  address: Address | null;
  chainId: number | null;
  status: WalletStatus;
  connecting: boolean;
  switching: boolean;
  error: string | null;
  /** Prompts the wallet for account access (eth_requestAccounts) */
  connect: () => Promise<void>;
  /** Forgets the account locally; injected wallets have no programmatic disconnect */
  disconnect: () => void;
  /** personal_signs the message with the connected account; throws when no wallet is connected */
  signMessage: (message: string) => Promise<Hex>;
  /** Requests a switch to `expectedChainId` (wallet_switchEthereumChain), then refreshes chainId */
  switchChain: (expectedChainId: number) => Promise<void>;
}

export const WalletContext = createContext<WalletContextValue | undefined>(
  undefined,
);

/** Reads the wallet context; throws outside a WalletProvider */
export function useWalletContext(): WalletContextValue {
  const ctx = useContext(WalletContext);
  if (!ctx)
    throw new Error("useWalletContext must be used within a <WalletProvider>.");
  return ctx;
}
