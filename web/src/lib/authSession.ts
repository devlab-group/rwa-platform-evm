// Admin JWT from POST /auth/session, issued after the configured admin signs
// a login challenge. Logout removes it locally; the server has no revocation.
//
// Persist in IndexedDB across reloads. An in-memory mirror supplies synchronous
// reads for client.ts headers and useSyncExternalStore; writes also update
// IndexedDB. main.tsx hydrates the mirror once at startup.
import { idbDelete, idbGet, idbSet } from "./idb";

const STORAGE_KEY = "authSession";

export interface AuthSession {
  /** Signed admin JWT, sent as `Authorization: Bearer <token>` */
  token: string;
  /** JWT expiry in Unix seconds */
  expiresAt: number;
  /** Always "admin" (the server issues a single role) */
  role: string;
  /** The authenticated admin wallet address, checksummed */
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

/** Returns the live session, or undefined after clearing an expired one */
export function getSession(): AuthSession | undefined {
  if (session && isExpired(session)) {
    // Expired in the mirror: drop it here and in IndexedDB
    session = undefined;
    void idbDelete(STORAGE_KEY);
  }
  return session;
}

/**
 * Sets the session, or clears it when `next` is undefined. The in-memory mirror
 * updates synchronously, so getSession reflects the change at once; the
 * IndexedDB write follows in the background.
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

/** Clears the session on logout, a 401 response, or expiry */
export function clearSession(): void {
  setSession(undefined);
}

/** Subscribes to session changes, for `useSyncExternalStore` in hooks/useAuthSession.ts */
export function subscribeSession(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/**
 * Loads a persisted JWT from IndexedDB into the in-memory mirror and notifies
 * subscribers. main.tsx starts it without awaiting, so the shell renders
 * first. An expired token is deleted instead of restored. Without IndexedDB it
 * resolves to a signed-out state.
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

// Test-only bootstrap. Playwright page objects call `page.goto(path)` per
// screen, which reloads the document and would force a login each time. The
// fixtures in tests/fixtures seed a session through `page.addInitScript`,
// which sets this global before any app code runs. Production code never sets
// `window.__RWA_E2E_SESSION__`; a script able to set it already has
// same-origin script execution.
declare global {
  interface Window {
    __RWA_E2E_SESSION__?: AuthSession;
  }
}

if (typeof window !== "undefined" && window.__RWA_E2E_SESSION__) {
  session = window.__RWA_E2E_SESSION__;
}
