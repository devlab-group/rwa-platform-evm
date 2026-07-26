// Admin session (wallet-signature login → admin JWT).
//
// The credential is now a stateless HMAC-signed JWT minted by
// `POST /auth/session` after the connected wallet signs a challenge whose
// recovered signer equals the configured project admin address (see
// components/Login.tsx). There is no server-side logout — logout just drops
// the token here.
//
// Storage: the JWT is persisted in IndexedDB (lib/idb.ts) so it survives a
// full page reload, which the prior in-memory-only operator session did not.
// Browser storage for this token is a deliberate, accepted tradeoff — an
// earlier design kept the session in memory only. To keep the synchronous
// getSession()/
// useSyncExternalStore contract (client.ts attaches the header synchronously),
// an in-memory mirror is the source of truth for reads and every write is
// write-through to IndexedDB. Call hydrateSession() once at startup to load a
// persisted token back into the mirror before the first render.
import { idbDelete, idbGet, idbSet } from "./idb";

const STORAGE_KEY = "authSession";

export interface AuthSession {
  /** Signed admin JWT — attached as `Authorization: Bearer <token>`. */
  token: string;
  /** Unix seconds; JWT expiry. */
  expiresAt: number;
  /** Always "admin" in V1 (single-admin model). */
  role: string;
  /** The authenticated admin wallet address (checksummed). */
  address: string;
}

type Listener = () => void;

let session: AuthSession | undefined;
const listeners = new Set<Listener>();

function notify(): void {
  for (const listener of listeners) listener();
}

function isExpired(s: AuthSession): boolean {
  return s.expiresAt * 1000 <= Date.now();
}

/** Returns the live session, clearing and returning undefined once it has expired. */
export function getSession(): AuthSession | undefined {
  if (session && isExpired(session)) {
    // Expired in the mirror — drop it here and in IndexedDB.
    session = undefined;
    void idbDelete(STORAGE_KEY);
  }
  return session;
}

/**
 * Sets (or, with undefined, clears) the session. Updates the in-memory mirror
 * synchronously (so getSession is immediately consistent) and write-through to
 * IndexedDB in the background.
 */
export function setSession(next: AuthSession | undefined): void {
  session = next;
  if (next) {
    void idbSet(STORAGE_KEY, next);
  } else {
    void idbDelete(STORAGE_KEY);
  }
  notify();
}

/** Clears the session — used on logout, 401, and expiry. */
export function clearSession(): void {
  setSession(undefined);
}

/** For `useSyncExternalStore` — see hooks/useAuthSession.ts. */
export function subscribeSession(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/**
 * Loads any persisted JWT from IndexedDB into the in-memory mirror. Awaited
 * once at startup (main.tsx) so the first render already reflects a returning
 * admin instead of flashing the login screen. A persisted-but-expired token is
 * dropped rather than restored. Safe to call in a non-IndexedDB environment
 * (resolves to a signed-out state).
 */
export async function hydrateSession(): Promise<void> {
  let stored: AuthSession | undefined;
  try {
    stored = await idbGet<AuthSession>(STORAGE_KEY);
  } catch {
    stored = undefined;
  }
  if (stored && !isExpired(stored)) {
    session = stored;
  } else if (stored) {
    void idbDelete(STORAGE_KEY);
    session = undefined;
  }
  notify();
}

// ---- e2e/test-only bootstrap ----------------------------------------------
// A real user's SPA navigation (NavLink clicks) never reloads the document, so
// the mirror survives it; Playwright's page-object `goto()` helpers do a full
// `page.goto(path)` per screen, which would otherwise force a fresh login on
// every navigation in the mocked harness. tests/fixtures seed a session
// directly via `page.addInitScript` (setting this global before any app code
// runs) instead of driving the login form each time. No production path sets
// `window.__RWA_E2E_SESSION__`; an attacker able to set it would already have
// full same-origin script execution.
declare global {
  interface Window {
    __RWA_E2E_SESSION__?: AuthSession;
  }
}

if (typeof window !== "undefined" && window.__RWA_E2E_SESSION__) {
  session = window.__RWA_E2E_SESSION__;
}
