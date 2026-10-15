import { WalletConnectButton } from "./WalletConnectButton";
import { NetworkSwitcher } from "./NetworkSwitcher";

/**
 * App navigation shares WalletProvider for connect and network controls; the switcher appears
 * when connected. The Investor route owns the page's <h1>.
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
