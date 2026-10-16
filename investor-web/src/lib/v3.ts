// Pure rules for the rwa-v3 surfaces: minimum purchase, oracle price availability, income claims
import type { components } from "./api-types";
import { formatTokenAmount } from "./format";

type Project = components["schemas"]["Project"];
type Inventory = components["schemas"]["Inventory"];

/** Minimum purchase in quote-token minimal units; 0n when the project sets none (rwa-v2 included) */
export function minPurchaseQuote(project: Project | undefined): bigint {
  const raw = project?.minPurchaseQuote;
  if (!raw) return 0n;
  try {
    return BigInt(raw);
  } catch {
    return 0n;
  }
}

/** Refusal text when the previewed quote is under the minimum, else null */
export function minPurchaseRefusal(
  quote: bigint,
  minimum: bigint,
  quoteDecimals: number | undefined,
): string | null {
  if (minimum === 0n || quote >= minimum) return null;
  return `This purchase quotes ${formatTokenAmount(quote.toString(), quoteDecimals)} quote tokens, below the project minimum of ${formatTokenAmount(minimum.toString(), quoteDecimals)}. Increase the amount.`;
}

const ORACLE_REASON: Record<string, string> = {
  StalePrice: "the price feed has not updated recently enough",
  InvalidPrice: "the price feed returned an invalid price",
  SequencerDown: "the L2 sequencer is down",
  GracePeriodNotOver:
    "the L2 sequencer restarted recently and the grace period has not passed",
  FeedDecimalsChanged: "the price feed changed its decimals",
  ReadFailed: "the price feed could not be read",
};

/** Human reason for an oracle error name, or a generic one for an unknown name */
export function oracleReason(name: string): string {
  return ORACLE_REASON[name] ?? "the price feed is unusable";
}

export interface PriceAvailability {
  unavailable: boolean;
  reason?: string;
}

/**
 * New buys and redemption requests stop when the oracle reports unavailable, the inventory carries
 * a priceError, or a preview reverted with an oracle error (previewError)
 */
export function priceAvailability(
  project: Project | undefined,
  inventory: Inventory | undefined,
  previewError?: string,
): PriceAvailability {
  const name =
    previewError ??
    (project?.strategyKind === "Oracle" && project.oracle?.available === false
      ? (project.oracle.error ?? "ReadFailed")
      : undefined) ??
    inventory?.priceError;
  return name
    ? { unavailable: true, reason: oracleReason(name) }
    : { unavailable: false };
}
