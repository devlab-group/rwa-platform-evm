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

/** Shared shell: brand, wallet controls, section nav, and the routed page. */
export function AppLayout() {
  const location = useLocation();
  const isAdmin = location.pathname.startsWith("/admin");
  const session = useAuthSession();
  // Every screen here is an admin screen and needs a live session; the check
  // stays path-scoped so a stray non-/admin URL still renders the shell.
  const requiresLogin = isAdmin && !session;
  const mainRef = useRef<HTMLElement>(null);
  const isFirstRender = useRef(true);

  // React Router doesn't manage focus on navigation (it's a client-side DOM
  // swap, not a real page load) — without this, a screen reader user who
  // clicks a nav link gets no indication anything happened. Skip it on the
  // very first render so a fresh page load still starts at the top of the
  // document (skip link first in tab order), and only shift focus on actual
  // route changes thereafter.
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

        {/* Global wallet affordance — present on every screen. On admin it's
            also the prerequisite for signing in (challenge/sign happens on the
            Login screen once a wallet is connected). The network switcher sits
            beside it and appears once a wallet is connected. */}
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
