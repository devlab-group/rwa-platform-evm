import type { ChangeEvent } from "react";
import { useWalletContext } from "../context/walletContextValue";
import { ALLOWED_NETWORKS, findNetwork } from "../lib/networks";

/**
 * Shows the connected wallet's network and the allowlist in lib/networks.ts.
 * switchChain adds an unknown chain through wallet_addEthereumChain before
 * switching; chainChanged keeps the displayed network synchronized. Hidden
 * when disconnected.
 */
export function NetworkSwitcher() {
  const { address, chainId, switchChain } = useWalletContext();

  if (!address) return null;

  const isSupported = chainId !== null && findNetwork(chainId) !== undefined;

  function handleChange(e: ChangeEvent<HTMLSelectElement>) {
    const next = Number(e.target.value);
    if (Number.isNaN(next) || next === chainId) return;
    void switchChain(next);
  }

  return (
    <div className="app-nav__network">
      <select
        className="app-nav__network-select"
        aria-label="Network"
        value={isSupported ? String(chainId) : "unsupported"}
        onChange={handleChange}
      >
        {/* A wallet chain outside the allow-list shows as a disabled, selected
            "Unsupported network" entry */}
        {!isSupported && (
          <option value="unsupported" disabled>
            Unsupported network
          </option>
        )}
        {ALLOWED_NETWORKS.map((network) => (
          <option key={network.chainId} value={String(network.chainId)}>
            {network.label}
          </option>
        ))}
      </select>
    </div>
  );
}
