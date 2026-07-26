import { WalletConnectButton } from "./WalletConnectButton";
import { NetworkSwitcher } from "./NetworkSwitcher";

/**
 * The app's only chrome: a brand label plus the wallet affordances. Both
 * children read the connected account and chain straight from WalletProvider,
 * so there is nothing to thread through — the switcher hides itself until a
 * wallet is connected.
 *
 * The page's own <h1> lives in the Investor route, so this stays a plain
 * banner with no heading of its own.
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
