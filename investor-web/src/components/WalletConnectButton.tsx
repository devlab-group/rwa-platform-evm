import { useWalletContext } from "../context/walletContextValue";
import { shortenAddress } from "../lib/format";
import { clearWalletSession } from "../lib/walletSession";

/**
 * Header wallet control: a "Connect wallet" button when no wallet is connected,
 * otherwise the shortened address and a Disconnect button.
 *
 * Disconnecting forgets the wallet locally and removes that address's stored
 * X-Wallet-Session, so the next connect needs a new challenge and signature.
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
