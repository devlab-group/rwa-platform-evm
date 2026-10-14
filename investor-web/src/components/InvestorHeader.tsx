import { WalletConnectButton } from "./WalletConnectButton";
import { NetworkSwitcher } from "./NetworkSwitcher";

/**
 * The app's only chrome: a brand label, the wallet connect button and the
 * network switcher. Both read the connected account and chain from
 * WalletProvider, and the switcher renders nothing until a wallet is connected.
 *
 * The page's <h1> lives in the Investor route, so this navigation landmark has
 * no heading of its own.
 */
export function InvestorHeader() {
  return (
    <nav className="app-nav" aria-label="Primary">
      <div className="app-nav__brand">RWA Investor</div>
      <WalletConnectButton />
      <NetworkSwitcher />
    </nav>
  );
}
