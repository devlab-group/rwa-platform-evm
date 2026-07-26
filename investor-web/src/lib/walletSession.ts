// Short-lived, wallet-scoped session token minted by
// POST /api/v1/compliance/challenge/verify (X-Wallet-Session; see
// api/openapi.yaml VerifyChallengeResult). It grants read access to a single
// address's own compliance status and nothing else — no issuer capability —
// which is what makes it safe to keep in an investor's browser at all.
// Stored per-address so a returning investor who reconnects the same wallet
// doesn't need to re-sign every page load until it expires.
const STORAGE_PREFIX = "rwa.walletSession.";

export interface WalletSession {
  token: string;
  expiresAt: string; // ISO8601
}

function storageKey(address: string): string {
  return `${STORAGE_PREFIX}${address.toLowerCase()}`;
}

/** Returns the stored session for `address` if present and not yet expired (clearing it if expired). */
export function getWalletSession(address: string): WalletSession | undefined {
  if (typeof window === "undefined") return undefined;
  const raw = window.localStorage.getItem(storageKey(address));
  if (!raw) return undefined;
  try {
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

export function setWalletSession(
  address: string,
  session: WalletSession,
): void {
  window.localStorage.setItem(storageKey(address), JSON.stringify(session));
}

export function clearWalletSession(address: string): void {
  window.localStorage.removeItem(storageKey(address));
}
