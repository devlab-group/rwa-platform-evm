import { useWalletContext } from "../context/walletContextValue";
import { clearSession } from "../lib/authSession";
import { shortenAddress } from "../lib/format";

/**
 * Global header wallet affordance, present on every screen. When no wallet is
 * connected it's the single "Connect wallet" entry point (and, on admin, the
 * prerequisite for signing in — see components/Login.tsx). When connected it
 * shows the shortened address and a Disconnect button.
 *
 * Disconnecting is a full sign-out: it forgets the wallet locally and drops
 * any admin JWT, since an admin session is only meaningful while the wallet
 * that proved the admin address stays connected.
 */
export function WalletConnectButton() {
  const wallet = useWalletContext();

  function handleDisconnect() {
    wallet.disconnect();
    clearSession();
  }

  if (!wallet.address) {
    return (
      <div className="app-nav__wallet">
        <button
          type="button"
          className="button button--secondary"
          onClick={wallet.connect}
          disabled={wallet.connecting}
        >
          {wallet.connecting ? "Connecting…" : "Connect wallet"}
        </button>
      </div>
    );
  }

  return (
    <div className="app-nav__wallet">
      <span className="app-nav__wallet-address" title={wallet.address}>
        {shortenAddress(wallet.address)}
      </span>
      <button
        type="button"
        className="button button--secondary"
        onClick={handleDisconnect}
      >
        Disconnect
      </button>
    </div>
  );
}
