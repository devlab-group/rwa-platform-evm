import { useWalletContext } from "../context/walletContextValue";
import { shortenAddress } from "../lib/format";
import { clearWalletSession } from "../lib/walletSession";

/**
 * Connects or shows address/Disconnect; disconnect clears local wallet state and
 * X-Wallet-Session, requiring a new ownership challenge on reconnect
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
