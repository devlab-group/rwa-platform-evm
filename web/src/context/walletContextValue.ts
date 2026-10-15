import { createContext, useContext } from "react";
import type { Address, Hex } from "viem";

export type WalletStatus = "disconnected" | "connecting" | "connected";

/** Shared wallet state; separate exports keep WalletContext.tsx compatible with react-refresh */
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
