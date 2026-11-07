import { sha256, toBytes, type Address, type Hex } from "viem";
import type { components } from "./api-types";
import type { ContractCall } from "./safeBatch";
import { erc20ApproveCall, escrowCall, type FundingPreview } from "./wallet";

type Redemption = components["schemas"]["Redemption"];

/**
 * Passes through exact 0x-prefixed bytes32 hex; hashes other non-empty reasons with SHA-256,
 * matching server EncodeReasonCode
 */
export function encodeReasonCode(input: string): Hex {
  if (!input) {
    throw new Error("A rejection reason code is required.");
  }
  if (/^0x[0-9a-fA-F]{64}$/.test(input)) {
    return input as Hex;
  }
  return sha256(toBytes(input));
}

/**
 * Orders exact-amount approval, funding and optional claim; atomic execution rolls back funding on
 * payout failure
 */
export function fundBatchCalls(
  quoteToken: Address,
  escrow: Address,
  id: bigint,
  quoteAmount: bigint,
  claim: boolean,
): ContractCall[] {
  return [
    erc20ApproveCall(quoteToken, escrow, quoteAmount),
    escrowCall(escrow, "fundRedemption", id),
    ...(claim ? [escrowCall(escrow, "claimRedemption", id)] : []),
  ];
}

/** Whether the request prices at funding (an rwa-v4 oracle request) rather than at request time */
export function isForwardRedemption(r: Pick<Redemption, "strategy">): boolean {
  return Boolean(r.strategy);
}

/** What to show as a request's quote */
export type RedemptionQuote =
  | { kind: "amount"; amount: string }
  /** A Pending forward request: quoteAmount is 0 until funding picks the round */
  | { kind: "pricedAtFunding"; minQuoteOut: string | undefined }
  /** A forward request that ended before it was priced */
  | { kind: "unpriced" };

const POSITIVE_INTEGER = /^\d+$/;

function isPositive(raw: string | undefined): raw is string {
  return raw !== undefined && POSITIVE_INTEGER.test(raw) && BigInt(raw) > 0n;
}

/**
 * Never presents a forward request's placeholder zero as a price: a Pending one is priced at
 * funding, one that ended unfunded was never priced, and a priced one shows its amount.
 */
export function redemptionQuote(
  r: Pick<Redemption, "strategy" | "status" | "quoteAmount" | "minQuoteOut">,
): RedemptionQuote {
  if (!isForwardRedemption(r) || isPositive(r.quoteAmount)) {
    return { kind: "amount", amount: r.quoteAmount ?? "" };
  }
  return r.status === "Pending"
    ? { kind: "pricedAtFunding", minQuoteOut: r.minQuoteOut }
    : { kind: "unpriced" };
}

/** Text for a RedemptionQuote; fmt formats quote-token minimal units */
export function redemptionQuoteLabel(
  q: RedemptionQuote,
  fmt: (minimalUnits: string) => string,
): string {
  switch (q.kind) {
    case "amount":
      return fmt(q.amount);
    case "pricedAtFunding":
      return isPositive(q.minQuoteOut)
        ? `Priced at funding (holder minimum ${fmt(q.minQuoteOut)})`
        : "Priced at funding (no holder minimum)";
    case "unpriced":
      return "Never priced";
  }
}

// Formats quote-token minimal units for a message
type Fmt = (minimalUnits: bigint | string) => string;

/** Operator-facing explanation of a funding preview that is not fundable (or the priced amount) */
export function fundingStateMessage(p: FundingPreview, fmt: Fmt): string {
  switch (p.state) {
    case "priced":
      return `Funding will pull ${fmt(p.amount)}.`;
    case "waiting":
      return "Waiting for a price round: no feed round past the dealing cut-off has landed yet. Funding opens once one does.";
    case "belowMin":
      return `The priced quote ${fmt(p.quoted)} is below the holder's minimum of ${fmt(p.minQuoteOut)}. Reject the request, or the holder can cancel once the timeout passes.`;
    case "closed":
      return "Funding closed when the redemption timeout was reached. The holder can cancel and recover their RWA.";
    case "blocked":
      return `Funding cannot go ahead: ${p.message}`;
  }
}

/**
 * The exact quote to approve and fund. A Pending forward request is priced by the contract, so the
 * amount comes from previewFunding, read now; every other request uses its recorded quoteAmount.
 */
export async function resolveFundingAmount(
  r: Pick<Redemption, "strategy" | "status" | "quoteAmount">,
  readPreview: () => Promise<FundingPreview>,
  fmt: Fmt,
): Promise<{ amount: bigint } | { error: string }> {
  if (isForwardRedemption(r) && r.status === "Pending") {
    const preview = await readPreview();
    return preview.state === "priced"
      ? { amount: preview.amount }
      : { error: fundingStateMessage(preview, fmt) };
  }
  if (isForwardRedemption(r)) {
    return isPositive(r.quoteAmount)
      ? { amount: BigInt(r.quoteAmount) }
      : {
          error: "This request was never priced, so there is nothing to fund.",
        };
  }
  return r.quoteAmount
    ? { amount: BigInt(r.quoteAmount) }
    : { error: "The recorded quote amount is missing. Check the indexer." };
}
