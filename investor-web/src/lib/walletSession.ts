// X-Wallet-Session from challenge verification authorizes one wallet's status
// and KYC start, with no admin capability. Tokens are stored per address in
// localStorage until server expiry (15 minutes by default), avoiding another
// signature on reconnection. Any same-origin script can read this storage;
// the wallet scope limits exposure.
const STORAGE_PREFIX = "rwa.walletSession.";

export interface WalletSession {
  token: string;
  expiresAt: string; // ISO 8601
}

function storageKey(address: string): string {
  return `${STORAGE_PREFIX}${address.toLowerCase()}`;
}

// Reading window.localStorage itself throws when the browser blocks site storage
function storage(): Storage | undefined {
  return typeof window === "undefined" ? undefined : window.localStorage;
}

/** Returns the stored session for `address` if present and not yet expired (clearing it if expired). */
export function getWalletSession(address: string): WalletSession | undefined {
  try {
    const raw = storage()?.getItem(storageKey(address));
    if (!raw) return undefined;
    const session = JSON.parse(raw) as WalletSession;
    if (!session.token || !session.expiresAt) return undefined;
    if (new Date(session.expiresAt).getTime() <= Date.now()) {
      clearWalletSession(address);
      return undefined;
    }
    return session;
  } catch {
    return undefined;
  }
}

/**
 * Stores the session for `address`. Blocked or full storage is not fatal: the session is
 * just not kept, and the investor signs the ownership challenge again next time.
 */
export function setWalletSession(
  address: string,
  session: WalletSession,
): void {
  try {
    storage()?.setItem(storageKey(address), JSON.stringify(session));
  } catch {
    // not persisted
  }
}

/** Removes the stored session for `address`, ignoring blocked storage */
export function clearWalletSession(address: string): void {
  try {
    storage()?.removeItem(storageKey(address));
  } catch {
    // nothing to clear
  }
}
