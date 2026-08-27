// Slippage-bound math for the investor buy/redeem flows. Kept separate from
// format.ts (display-only) since these values are submitted on-chain.

/**
 * Upper cap on the slippage tolerance accepted on either side of a trade.
 *
 * On a purchase it bounds the ERC-20 approve() amount — how much quote token
 * the investor authorizes the Vault to spend — so a fat-fingered value (e.g. a
 * stray extra digit) cannot silently approve far more than intended. On a
 * redemption it bounds minQuoteOut; without a cap, 10000 bps means
 * minQuoteOut == 0, i.e. accepting any price at all. 5000 bps (50%) is already
 * a very loose tolerance; nothing legitimate needs more.
 */
export const MAX_SLIPPAGE_BPS = 5000;

/** Upper bound on quote token spend for a purchase: quote * (1 + slippageBps/10000), rounded up, capped at MAX_SLIPPAGE_BPS. */
export function applySlippageCeil(amount: string, slippageBps: number): string {
  const base = BigInt(amount);
  const bps = BigInt(
    Math.max(0, Math.min(MAX_SLIPPAGE_BPS, Math.round(slippageBps))),
  );
  return ((base * (10_000n + bps) + 9_999n) / 10_000n).toString();
}

/** Lower bound on quote token received for a redemption: quote * (1 - slippageBps/10000), rounded down, capped at MAX_SLIPPAGE_BPS. */
export function applySlippageFloor(
  amount: string,
  slippageBps: number,
): string {
  const base = BigInt(amount);
  const bps = BigInt(
    Math.max(0, Math.min(MAX_SLIPPAGE_BPS, Math.round(slippageBps))),
  );
  return ((base * (10_000n - bps)) / 10_000n).toString();
}

/** Unix-seconds deadline `minutes` from now, for the buy/redeem calls that take one. */
export function deadlineInMinutes(minutes: number): number {
  return Math.floor(Date.now() / 1000) + minutes * 60;
}
