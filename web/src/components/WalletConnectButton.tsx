import { useWalletContext } from "../context/walletContextValue";
import { clearSession } from "../lib/authSession";
import { shortenAddress } from "../lib/format";

/**
 * Header wallet control, shown on every screen. Without a connected wallet it
 * is a "Connect wallet" button. With one it shows the shortened address and a
 * Disconnect button.
 *
 * Disconnect also clears the admin JWT: it forgets the wallet locally and signs
 * the admin out.
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
