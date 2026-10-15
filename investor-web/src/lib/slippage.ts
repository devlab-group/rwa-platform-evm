// On-chain slippage bounds for buy/redeem; kept separate from display formatting

/**
 * Caps purchase approval and redemption payout reduction in basis points;
 * 10000 bps would remove the payout floor entirely
 */
export const MAX_SLIPPAGE_BPS = 5000;

/** Purchase ceiling: round up quote * (1 + bps/10000); clamp bps to 0..MAX_SLIPPAGE_BPS */
export function applySlippageCeil(amount: string, slippageBps: number): string {
  const base = BigInt(amount);
  const bps = BigInt(
    Math.max(0, Math.min(MAX_SLIPPAGE_BPS, Math.round(slippageBps))),
  );
  return ((base * (10_000n + bps) + 9_999n) / 10_000n).toString();
}

/** Redemption floor: round down quote * (1 - bps/10000); clamp bps to 0..MAX_SLIPPAGE_BPS */
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

/** Buy/redemption-request deadline in Unix seconds, minutes from now */
export function deadlineInMinutes(minutes: number): number {
  return Math.floor(Date.now() / 1000) + minutes * 60;
}
