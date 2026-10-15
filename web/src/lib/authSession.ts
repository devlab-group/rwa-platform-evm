// Admin JWT from /auth/session; logout is local, without server revocation.
// IndexedDB survives reloads, while a memory mirror serves synchronous client/hook reads.
// Writes update memory first; main.tsx starts hydration at startup.
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
 * Updates the memory mirror synchronously, then writes/deletes IndexedDB in the background;
 * undefined clears
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
 * Hydrates memory from IndexedDB and notifies subscribers; expired entries are deleted.
 * main.tsx does not await it, so the shell renders first; unavailable storage leaves a fresh
 * page signed out.
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

// Playwright seeds window.__RWA_E2E_SESSION__ through addInitScript on document loads.
// Production does not populate it; writing it requires same-origin script execution.
declare global {
  interface Window {
    __RWA_E2E_SESSION__?: AuthSession;
  }
}

if (typeof window !== "undefined" && window.__RWA_E2E_SESSION__) {
  session = window.__RWA_E2E_SESSION__;
}
