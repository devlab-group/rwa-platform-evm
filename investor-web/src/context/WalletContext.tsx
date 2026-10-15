import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import type { Address, Hex } from "viem";
import {
  connectWallet,
  getInjectedProvider,
  requestSwitchChain,
  signMessage as signMessageWithWallet,
} from "../lib/wallet";
import {
  WalletContext,
  type WalletContextValue,
  type WalletStatus,
} from "./walletContextValue";

/**
 * Shares wallet state and accountsChanged, chainChanged and disconnect listeners app-wide.
 * Context types and hook live in walletContextValue so this file exports only a component
 * for react-refresh.
 */
export function WalletProvider({ children }: { children: ReactNode }) {
  const [address, setAddress] = useState<Address | null>(null);
  const [chainId, setChainId] = useState<number | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [switching, setSwitching] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const connect = useCallback(async () => {
    setConnecting(true);
    setError(null);
    try {
      const wallet = await connectWallet();
      setAddress(wallet.address);
      setChainId(wallet.chainId);
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to connect wallet.",
      );
    } finally {
      setConnecting(false);
    }
  }, []);

  const disconnect = useCallback(() => {
    // Forget local connection state; WalletConnectButton clears the stored session
    setAddress(null);
    setChainId(null);
    setError(null);
  }, []);

  const signMessage = useCallback(
    (message: string): Promise<Hex> => {
      if (!address) throw new Error("No wallet connected.");
      return signMessageWithWallet(address, message);
    },
    [address],
  );

  const switchChain = useCallback(async (expectedChainId: number) => {
    setSwitching(true);
    setError(null);
    try {
      await requestSwitchChain(expectedChainId);
      // Refresh chainId even if the wallet emits no chainChanged event
      const provider = getInjectedProvider();
      if (provider) {
        const chainIdHex = (await provider.request({
          method: "eth_chainId",
        })) as string;
        setChainId(Number(chainIdHex));
      }
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to switch network.",
      );
    } finally {
      setSwitching(false);
    }
  }, []);

  // Follow account, chain and disconnect events; initial state is disconnected,
  // but accountsChanged can set an address before an explicit connect
  useEffect(() => {
    const provider = getInjectedProvider();
    if (!provider?.on) return;

    const handleChainChanged = (...args: unknown[]) => {
      setChainId(Number(args[0] as string));
    };
    const handleAccountsChanged = (...args: unknown[]) => {
      const accounts = args[0] as string[];
      if (accounts.length === 0) {
        setAddress(null);
        setChainId(null);
      } else {
        setAddress(accounts[0] as Address);
      }
    };
    const handleDisconnect = () => {
      setAddress(null);
      setChainId(null);
    };

    provider.on("chainChanged", handleChainChanged);
    provider.on("accountsChanged", handleAccountsChanged);
    provider.on("disconnect", handleDisconnect);
    return () => {
      provider.removeListener?.("chainChanged", handleChainChanged);
      provider.removeListener?.("accountsChanged", handleAccountsChanged);
      provider.removeListener?.("disconnect", handleDisconnect);
    };
  }, []);

  const status: WalletStatus = connecting
    ? "connecting"
    : address
      ? "connected"
      : "disconnected";

  const value = useMemo<WalletContextValue>(
    () => ({
      address,
      chainId,
      status,
      connecting,
      switching,
      error,
      connect,
      disconnect,
      signMessage,
      switchChain,
    }),
    [
      address,
      chainId,
      status,
      connecting,
      switching,
      error,
      connect,
      disconnect,
      signMessage,
      switchChain,
    ],
  );

  return (
    <WalletContext.Provider value={value}>{children}</WalletContext.Provider>
  );
}
