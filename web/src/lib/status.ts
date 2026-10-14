// UI status derivation. Chain state from api-types is the source of truth; this
// module adds display labels and splits Funded into Funded and Claimable.
import type { components } from "./api-types";

export type RedemptionChainStatus = NonNullable<
  components["schemas"]["Redemption"]["status"]
>;
export type TransactionChainStatus = NonNullable<
  components["schemas"]["Transaction"]["status"]
>;

/** Confirmation threshold used when a response carries neither `claimable` nor `finalityConfirmations` */
export const FALLBACK_FINALITY_CONFIRMATIONS = 12;

export type RedemptionDisplayStatus =
  | "Pending"
  | "Funded"
  | "Claimable"
  | "Completed"
  | "Rejected"
  | "Cancelled"
  | "Unknown";

/**
 * Maps the on-chain redemption status to a display status. The contract allows
 * a claim as soon as a redemption is Funded, but the indexer's view of the
 * funding transaction is unconfirmed until it reaches the finality threshold.
 * A Funded redemption therefore shows as Claimable only when the server's
 * `claimable` flag is true (Funded and confirmations >=
 * Project.finalityConfirmations). Without that flag, `confirmations` is
 * compared to `finalityConfirmations`, or to FALLBACK_FINALITY_CONFIRMATIONS.
 */
export function redemptionDisplayStatus(
  status: RedemptionChainStatus | undefined,
  opts: {
    claimable?: boolean;
    confirmations?: number;
    finalityConfirmations?: number;
  } = {},
): RedemptionDisplayStatus {
  switch (status) {
    case "Pending":
      return "Pending";
    case "Funded": {
      const claimable =
        opts.claimable ??
        (opts.confirmations ?? 0) >=
          (opts.finalityConfirmations ?? FALLBACK_FINALITY_CONFIRMATIONS);
      return claimable ? "Claimable" : "Funded";
    }
    case "Completed":
      return "Completed";
    case "Rejected":
      return "Rejected";
    case "Cancelled":
      return "Cancelled";
    default:
      return "Unknown";
  }
}

export const REDEMPTION_STATUS_LABEL: Record<RedemptionDisplayStatus, string> =
  {
    Pending: "Pending",
    Funded: "Funded (confirming)",
    Claimable: "Claimable",
    Completed: "Completed",
    Rejected: "Rejected",
    Cancelled: "Cancelled",
    Unknown: "Unknown",
  };

/**
 * Label for every transaction status in the OpenAPI enum. The map is keyed by
 * the generated `TransactionChainStatus` union, so adding an enum value fails
 * typecheck until this map (and `TRANSACTION_TONE`) covers it. For a status
 * outside the union, such as one a newer server adds, use
 * `transactionStatusLabel`, which returns "Unknown".
 */
export const TRANSACTION_STATUS_LABEL: Record<TransactionChainStatus, string> =
  {
    pending: "Unconfirmed",
    mined: "Mined (confirming)",
    confirmed: "Confirmed",
    replaced: "Replaced",
    reverted: "Reverted - failed on-chain",
    reorged: "Reorged - resubmission required",
    failed: "Failed - never broadcast",
    broadcast_unknown: "Broadcast unknown - do not resubmit",
    nonce_consumed_externally: "Nonce consumed externally",
    needs_intervention: "Needs operator intervention",
  };

/** Label for a transaction status the client does not recognize */
export const TRANSACTION_STATUS_UNKNOWN_LABEL = "Unknown";

/**
 * Transaction states that need an operator: the attempt reverted on-chain
 * (`reverted`), the node rejected it before it reached the mempool (`failed`),
 * its block left the canonical chain (`reorged`), whether the node received it
 * is unknown and it must not be blindly resubmitted (`broadcast_unknown`),
 * another transaction used its nonce (`nonce_consumed_externally`), or it
 * exhausted its replacement attempts (`needs_intervention`). `pending`,
 * `mined`, `confirmed` and `replaced` are excluded.
 */
const TRANSACTION_ATTENTION: ReadonlySet<TransactionChainStatus> = new Set([
  "reverted",
  "reorged",
  "failed",
  "broadcast_unknown",
  "nonce_consumed_externally",
  "needs_intervention",
]);

/** Whether `status` is one of the statuses this client knows */
export function isKnownTransactionStatus(
  status: string,
): status is TransactionChainStatus {
  return Object.prototype.hasOwnProperty.call(TRANSACTION_STATUS_LABEL, status);
}

/** Display label for any status string; one outside the generated enum gets "Unknown" */
export function transactionStatusLabel(status: string): string {
  return isKnownTransactionStatus(status)
    ? TRANSACTION_STATUS_LABEL[status]
    : TRANSACTION_STATUS_UNKNOWN_LABEL;
}

/**
 * True for the statuses in `TRANSACTION_ATTENTION`. An unrecognized status also
 * returns true, since the client cannot tell that it is safe. Transaction lists
 * use it to show a page-level warning instead of relying on one row's badge.
 */
export function needsAttention(status: string): boolean {
  return !isKnownTransactionStatus(status) || TRANSACTION_ATTENTION.has(status);
}

/**
 * True when `fundRedemption` on a Pending redemption would revert because the
 * recorded beneficiary is not currently Allowed. It applies only to Pending
 * requests: `claimRedemption` does not check compliance, so a later status
 * change does not stop payout of a Funded request.
 *
 * `beneficiaryAllowed` is optional in the API; when absent this returns false.
 */
export function redemptionBlockedByCompliance(
  status: RedemptionChainStatus | undefined,
  beneficiaryAllowed: boolean | undefined,
): boolean {
  return status === "Pending" && beneficiaryAllowed === false;
}

export type ComplianceChainStatus = NonNullable<
  components["schemas"]["WalletStatus"]["status"]
>;

export type ComplianceDisplayStatus =
  "Allowed" | "Expired" | "Blocked" | "NotOnboarded";

/**
 * Returns the compliance status for display, checking expiry at `nowSeconds`.
 * An `Allowed` record keeps that status in storage after a nonzero `validUntil`
 * passes, but the registry's `isAllowed` then returns false, so the display
 * status is `Expired`.
 */
export function complianceDisplayStatus(
  status: ComplianceChainStatus | undefined,
  validUntil: number | undefined,
  nowSeconds: number = Math.floor(Date.now() / 1000),
): ComplianceDisplayStatus {
  switch (status) {
    case "Allowed":
      return validUntil && validUntil < nowSeconds ? "Expired" : "Allowed";
    case "Blocked":
      return "Blocked";
    default:
      return "NotOnboarded";
  }
}

export const COMPLIANCE_STATUS_LABEL: Record<ComplianceDisplayStatus, string> =
  {
    Allowed: "Allowed",
    Expired: "Expired",
    Blocked: "Blocked",
    NotOnboarded: "Not onboarded",
  };
