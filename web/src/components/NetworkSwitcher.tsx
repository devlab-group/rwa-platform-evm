import type { ChangeEvent } from "react";
import { useWalletContext } from "../context/walletContextValue";
import { ALLOWED_NETWORKS, findNetwork } from "../lib/networks";

/**
 * In-app network switcher: a small select in the admin header showing the
 * wallet's current network and offering the allow-listed networks
 * (lib/networks.ts). Picking one calls the context's switchChain() — which
 * issues wallet_switchEthereumChain and, for a chain the wallet doesn't know
 * yet (e.g. Local anvil), adds it first (see lib/wallet.ts). The current chain
 * is rendered straight from the context, which tracks `chainChanged`, so a
 * switch made in the wallet itself is reflected here too. Absent when no wallet
 * is connected (there's no chain to show or switch).
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
        {/* A chain the wallet is on that we don't offer shows as an inert,
            selected "Unsupported network" entry (can't be re-picked). */}
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
