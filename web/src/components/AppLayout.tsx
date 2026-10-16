import { Suspense, useEffect, useRef, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { Login } from "./Login";
import { SessionControls } from "./SessionControls";
import { WalletConnectButton } from "./WalletConnectButton";
import { NetworkSwitcher } from "./NetworkSwitcher";
import { useAuthSession } from "../hooks/useAuthSession";
import { api } from "../lib/client";

const ADMIN_LINKS = [
  { to: "/admin/setup", label: "Setup" },
  { to: "/admin/assets", label: "Assets" },
  { to: "/admin/compliance", label: "Compliance" },
  { to: "/admin/inventory-sales", label: "Inventory & Sales" },
  { to: "/admin/redemptions", label: "Redemptions" },
  { to: "/admin/income", label: "Income", rwaV3Only: true },
  { to: "/admin/transactions", label: "Transactions" },
  { to: "/admin/security", label: "Security" },
] as { to: string; label: string; rwaV3Only?: boolean }[];

/** Shared shell: brand, wallet controls, section nav and the routed page */
export function AppLayout() {
  const location = useLocation();
  const isAdmin = location.pathname.startsWith("/admin");
  const session = useAuthSession();
  // Require a live admin session for /admin paths; other paths still render the shell
  const requiresLogin = isAdmin && !session;
  // Show Income for rwa-v3. Retry unknown versions on navigation to catch in-session
  // deployment; retain known versions to avoid further requests.
  const signedIn = Boolean(session);
  const [version, setVersion] = useState<string>();
  useEffect(() => {
    if (!signedIn || version) return;
    const controller = new AbortController();
    api
      .getProject({ signal: controller.signal })
      .then((p) => setVersion(p.version))
      .catch(() => undefined);
    return () => controller.abort();
  }, [signedIn, version, location.pathname]);
  const links = ADMIN_LINKS.filter((l) => !l.rwaV3Only || version === "rwa-v3");
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
            {links.map((link) => (
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
