import type { ChangeEvent } from "react";
import { useWalletContext } from "../context/walletContextValue";
import { ALLOWED_NETWORKS, findNetwork } from "../lib/networks";

/**
 * Shows the connected network and lib/networks.ts choices; hidden when disconnected.
 * Context switchChain handles unknown networks, and chainChanged updates the selection.
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
        {/* An unlisted current chain appears as selected, disabled "Unsupported network" */}
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
