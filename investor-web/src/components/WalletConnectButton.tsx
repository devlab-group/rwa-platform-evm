import { useWalletContext } from "../context/walletContextValue";
import { shortenAddress } from "../lib/format";
import { clearWalletSession } from "../lib/walletSession";

/**
 * Header wallet affordance. With no wallet connected it's the single "Connect
 * wallet" entry point; connected, it shows the shortened address and a
 * Disconnect button.
 *
 * Disconnecting is a full sign-out: it forgets the wallet locally and drops
 * that address's stored X-Wallet-Session, since the session only means
 * anything while the wallet that proved ownership stays connected. The next
 * connect re-runs the challenge/sign exchange.
 */
export function WalletConnectButton() {
  const wallet = useWalletContext();

  function handleDisconnect() {
    const { address } = wallet;
    wallet.disconnect();
    if (address) clearWalletSession(address);
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
