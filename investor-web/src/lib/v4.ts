// Pure rules for the rwa-v4 forward-priced surfaces: instant versus forward buying, deposit
// sizing, order state and actions, and redemption quote display
import type { components } from "./api-types";
import { formatTokenAmount } from "./format";
import { applySlippageCeil } from "./slippage";
import { oracleReason } from "./v3";

type Project = components["schemas"]["Project"];
type PurchaseOrder = components["schemas"]["PurchaseOrder"];
type Redemption = components["schemas"]["Redemption"];

const UINT80_MAX = (1n << 80n) - 1n;

/** Forward flow only when the server says so; absent or false keeps the instant buy flow */
export function isForwardPriced(project: Project | undefined): boolean {
  return project?.forwardPriced === true;
}

/** Parse an unsigned decimal string as bigint; return null for undefined or invalid text */
export function parseUint(value: string | undefined): bigint | null {
  if (value === undefined || !/^\d+$/.test(value)) return null;
  return BigInt(value);
}

/** Longest whole unit that divides the span evenly, such as "2 hours" or "90 minutes" */
export function formatDuration(seconds: number): string {
  if (!Number.isInteger(seconds) || seconds <= 0) return "";
  const units: [string, number][] = [
    ["day", 86_400],
    ["hour", 3_600],
    ["minute", 60],
    ["second", 1],
  ];
  const [name, size] = units.find(([, s]) => seconds % s === 0)!;
  const count = seconds / size;
  return `${count} ${name}${count === 1 ? "" : "s"}`;
}

export interface ForwardDeposit {
  deposit: bigint;
  /** Why the order cannot be placed, or null */
  refusal: string | null;
  /** The estimate is below the minimum; settlement at this price would refund the deposit */
  estimateBelowMinimum: boolean;
}

/**
 * Deposit for a forward order: the previewed estimate plus the slippage margin, which is the
 * Vault's maxQuoteAmount. The Vault rejects a deposit under the minimum or of zero.
 */
export function forwardDeposit(
  estimate: bigint,
  slippageBps: number,
  minimum: bigint,
  quoteDecimals: number | undefined,
): ForwardDeposit {
  const deposit = BigInt(applySlippageCeil(estimate.toString(), slippageBps));
  let refusal: string | null = null;
  if (deposit === 0n) {
    refusal =
      "The estimated quote is zero, so there is nothing to deposit. Increase the amount.";
  } else if (minimum > 0n && deposit < minimum) {
    refusal = `The deposit of ${formatTokenAmount(deposit.toString(), quoteDecimals)} quote tokens is below the project minimum of ${formatTokenAmount(minimum.toString(), quoteDecimals)}. Increase the amount or the slippage.`;
  }
  return {
    deposit,
    refusal,
    estimateBelowMinimum: minimum > 0n && estimate < minimum,
  };
}

/** What reading the order's own strategy says about its pricing round */
export type OrderRound =
  | { state: "priced"; quote: bigint; roundId: bigint }
  | { state: "waiting" }
  | { state: "unusable"; reason: string }
  | { state: "unknown" };

/**
 * RoundNotAvailable covers missing or unreadable rounds; map it to waiting.
 * Map other contract reverts to unusable.
 */
export function roundFromRevert(errorName: string): OrderRound {
  return errorName === "RoundNotAvailable"
    ? { state: "waiting" }
    : { state: "unusable", reason: oracleReason(errorName) };
}

export interface OrderReadArgs {
  tokenAmount: bigint;
  baseRound: bigint;
  createdAt: bigint;
}

/**
 * Parse unsigned tokenAmount and baseRound, require a uint80 baseRound and a nonnegative
 * safe-integer createdAt. Return null if these checks fail.
 */
export function orderReadArgs(
  order: Pick<PurchaseOrder, "tokenAmount" | "baseRound" | "createdAt">,
): OrderReadArgs | null {
  const tokenAmount = parseUint(order.tokenAmount);
  const baseRound = parseUint(order.baseRound);
  if (tokenAmount === null || baseRound === null || baseRound > UINT80_MAX)
    return null;
  // A safe integer always fits uint64
  if (!Number.isSafeInteger(order.createdAt) || order.createdAt < 0)
    return null;
  return { tokenAmount, baseRound, createdAt: BigInt(order.createdAt) };
}

export type SettlementOutlook =
  | { outcome: "fill"; paid: bigint; refund: bigint }
  | {
      outcome: "refund";
      refund: bigint;
      reason: "above-deposit" | "below-minimum";
    };

/**
 * What settling at a quote does, from the Vault's rule: it fills when the quote is at most the
 * deposit and at least the current minimum, else it returns the whole deposit. Inventory and
 * recipient eligibility can also force a refund and are not visible here.
 */
export function settlementOutlook(
  quote: bigint,
  deposit: bigint,
  minimum: bigint,
): SettlementOutlook {
  if (quote > deposit)
    return { outcome: "refund", refund: deposit, reason: "above-deposit" };
  if (quote < minimum)
    return { outcome: "refund", refund: deposit, reason: "below-minimum" };
  return { outcome: "fill", paid: quote, refund: deposit - quote };
}

export type OrderNote =
  | "buyer-not-allowed"
  | "paused"
  | "unreadable"
  | "cancel-opens-later"
  | "not-buyer";

export interface OrderActions {
  settle: boolean;
  cancel: boolean;
  note: OrderNote | null;
}

export interface OrderContext {
  nowSeconds: number;
  /** Project paused; settlement reverts while it is */
  paused: boolean | undefined;
  /** Buyer is Allowed; undefined when the lookup failed, which leaves the contract to decide */
  buyerAllowed: boolean | undefined;
  /** The connected wallet placed the order; only the buyer can cancel */
  isBuyer: boolean;
}

/**
 * Choose UI actions for Pending orders. A priced round enables settlement unless pause is
 * reported; waiting or unusable pricing enables buyer cancellation from timeoutAt, inclusive.
 * Unknown pricing or a buyer known to be not Allowed blocks both actions.
 */
export function orderActions(
  order: Pick<PurchaseOrder, "status" | "timeoutAt">,
  round: OrderRound,
  ctx: OrderContext,
): OrderActions {
  const none = (note: OrderNote | null): OrderActions => ({
    settle: false,
    cancel: false,
    note,
  });
  if (order.status !== "Pending") return none(null);
  if (ctx.buyerAllowed === false) return none("buyer-not-allowed");
  switch (round.state) {
    case "priced":
      return ctx.paused
        ? none("paused")
        : { settle: true, cancel: false, note: null };
    case "unknown":
      return none("unreadable");
    default: {
      const open =
        Number.isFinite(order.timeoutAt) && ctx.nowSeconds >= order.timeoutAt;
      if (!open) return none("cancel-opens-later");
      if (!ctx.isBuyer) return none("not-buyer");
      return { settle: false, cancel: true, note: null };
    }
  }
}

export type RedemptionQuoteCell =
  | { kind: "amount"; raw: string | undefined }
  | { kind: "atFunding"; minQuoteOut: string | null }
  | { kind: "none" };

/**
 * Quote column of a redemption row. A forward request stores a quote of 0 until funding prices
 * it, and an instant request never has a zero quote, so a Pending zero is a request awaiting its
 * price and must not read as a payout.
 */
export function redemptionQuoteCell(
  r: Pick<Redemption, "status" | "quoteAmount" | "strategy" | "minQuoteOut">,
): RedemptionQuoteCell {
  const quote = parseUint(r.quoteAmount);
  if (quote !== null && quote > 0n)
    return { kind: "amount", raw: r.quoteAmount };
  const unpriced = quote === 0n || Boolean(r.strategy);
  if (!unpriced) return { kind: "amount", raw: r.quoteAmount };
  if (r.status !== "Pending") return { kind: "none" };
  return {
    kind: "atFunding",
    minQuoteOut: parseUint(r.minQuoteOut) === null ? null : r.minQuoteOut!,
  };
}
