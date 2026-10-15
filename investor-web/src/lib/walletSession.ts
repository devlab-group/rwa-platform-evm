// Per-address localStorage sessions authorize own-status reads and KYC, without admin authority.
// Expired entries (15-minute default TTL) are cleared on read; Disconnect clears the current wallet.
// Same-origin scripts can read them; wallet scope limits exposure.
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

/** Returns a stored session unless expired; clears expired entries */
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
 * Stores a per-address session; blocked/full storage is ignored, so a later visit may
 * require another ownership challenge
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

/** Removes the session, ignoring blocked storage */
export function clearWalletSession(address: string): void {
  try {
    storage()?.removeItem(storageKey(address));
  } catch {
    // nothing to clear
  }
}
