import { Suspense, useEffect, useRef } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { Login } from "./Login";
import { SessionControls } from "./SessionControls";
import { WalletConnectButton } from "./WalletConnectButton";
import { NetworkSwitcher } from "./NetworkSwitcher";
import { useAuthSession } from "../hooks/useAuthSession";

const ADMIN_LINKS = [
  { to: "/admin/setup", label: "Setup" },
  { to: "/admin/assets", label: "Assets" },
  { to: "/admin/compliance", label: "Compliance" },
  { to: "/admin/inventory-sales", label: "Inventory & Sales" },
  { to: "/admin/redemptions", label: "Redemptions" },
  { to: "/admin/transactions", label: "Transactions" },
  { to: "/admin/security", label: "Security" },
];

/** Shared shell: brand, wallet controls, section nav and the routed page */
export function AppLayout() {
  const location = useLocation();
  const isAdmin = location.pathname.startsWith("/admin");
  const session = useAuthSession();
  // Require a live admin session for /admin paths; other paths still render the shell
  const requiresLogin = isAdmin && !session;
  const mainRef = useRef<HTMLElement>(null);
  const isFirstRender = useRef(true);

  // Focus main after route changes for screen readers; React Router does not move focus.
  // Skip the first render to preserve initial skip-link navigation.
  useEffect(() => {
    if (isFirstRender.current) {
      isFirstRender.current = false;
      return;
    }
    mainRef.current?.focus();
  }, [location.pathname]);

  return (
    <div className="app-shell">
      <a href="#main-content" className="skip-link">
        Skip to main content
      </a>

      <nav className="app-nav" aria-label="Primary">
        <div className="app-nav__brand">RWA Platform</div>

        {/* Shared wallet controls; show the network switcher when connected */}
        <WalletConnectButton />
        <NetworkSwitcher />

        {isAdmin && (
          <ul className="app-nav__links">
            {ADMIN_LINKS.map((link) => (
              <li key={link.to}>
                <NavLink
                  to={link.to}
                  className={({ isActive }) => (isActive ? "active" : "")}
                >
                  {link.label}
                </NavLink>
              </li>
            ))}
          </ul>
        )}

        {isAdmin && <SessionControls />}
      </nav>

      <main id="main-content" className="app-main" tabIndex={-1} ref={mainRef}>
        <Suspense
          fallback={
            <div className="async-state async-state--loading" role="status">
              Loading…
            </div>
          }
        >
          {requiresLogin ? <Login /> : <Outlet />}
        </Suspense>
      </main>
    </div>
  );
}
