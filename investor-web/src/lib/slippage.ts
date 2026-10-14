// Slippage-bound math for the investor buy and redeem flows. Kept apart from
// format.ts (display only) because these values are submitted on-chain.

/**
 * Upper cap on the slippage tolerance, in basis points, on either side of a
 * trade.
 *
 * On a purchase it bounds the ERC-20 approve() amount, so a mistyped value
 * (e.g. a stray extra digit) cannot approve far more quote token than
 * intended. On a redemption it bounds how low minQuoteOut can go; uncapped,
 * 10000 bps gives minQuoteOut == 0, which accepts any price.
 */
export const MAX_SLIPPAGE_BPS = 5000;

/** Upper bound on quote token spend for a purchase: quote * (1 + slippageBps/10000), rounded up, with slippageBps clamped to 0..MAX_SLIPPAGE_BPS. */
export function applySlippageCeil(amount: string, slippageBps: number): string {
  const base = BigInt(amount);
  const bps = BigInt(
    Math.max(0, Math.min(MAX_SLIPPAGE_BPS, Math.round(slippageBps))),
  );
  return ((base * (10_000n + bps) + 9_999n) / 10_000n).toString();
}

/** Lower bound on quote token received for a redemption: quote * (1 - slippageBps/10000), rounded down, with slippageBps clamped to 0..MAX_SLIPPAGE_BPS. */
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

/** Unix-seconds deadline `minutes` from now, for the buy and redemption-request calls. */
export function deadlineInMinutes(minutes: number): number {
  return Math.floor(Date.now() / 1000) + minutes * 60;
}
