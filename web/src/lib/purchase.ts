// Helpers for rwa-v4 purchase orders and their reserved deposits
import type { components } from "./api-types";
import type { OrderPricing } from "./wallet";

type Inventory = components["schemas"]["Inventory"];

const DIGITS = /^\d+$/;

/**
 * Calculate the displayed withdrawable quote: balance minus open deposits, clamped to zero.
 * An absent or invalid balance returns undefined; absent or invalid pending deposits count as zero.
 */
export function withdrawableQuote(
  inv: Pick<Inventory, "quoteBalance" | "pendingPurchaseQuote">,
): bigint | undefined {
  if (inv.quoteBalance === undefined || !DIGITS.test(inv.quoteBalance)) {
    return undefined;
  }
  const balance = BigInt(inv.quoteBalance);
  const pending =
    inv.pendingPurchaseQuote !== undefined &&
    DIGITS.test(inv.pendingPurchaseQuote)
      ? BigInt(inv.pendingPurchaseQuote)
      : 0n;
  return balance > pending ? balance - pending : 0n;
}

/**
 * Operator-facing line for what a Pending order would settle at. Settlement refunds the whole
 * deposit when the quote is above the deposit or below the minimum recorded with the order.
 */
export function orderPricingMessage(
  p: OrderPricing,
  fmt: (minimalUnits: bigint) => string,
  bounds: { deposit: bigint; minQuote: bigint },
): string {
  switch (p.state) {
    case "priced": {
      const priced = `Priced at round ${p.roundId}: ${fmt(p.quoteAmount)}`;
      const refund =
        "Settling will refund the whole deposit and the buyer receives no RWA.";
      if (p.quoteAmount > bounds.deposit) {
        return `${priced}, above the buyer's deposit of ${fmt(bounds.deposit)}. ${refund}`;
      }
      if (p.quoteAmount < bounds.minQuote) {
        return `${priced}, below this order's minimum of ${fmt(bounds.minQuote)}. ${refund}`;
      }
      return `${priced}. Anyone can settle it.`;
    }
    case "waiting":
      return "Waiting for a price round: none has landed past the dealing cut-off yet.";
    case "blocked":
      return `The pricing round cannot be used: ${p.message}`;
  }
}
