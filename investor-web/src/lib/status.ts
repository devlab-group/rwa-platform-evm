// UI-level status derivation. Chain state (from api-types) is the source of
// truth; this module only adds display labels and the Funded -> Claimable
// split, so the UI can tell Pending, Funded and Claimable apart.
import type { components } from "./api-types";

export type RedemptionChainStatus = NonNullable<
  components["schemas"]["Redemption"]["status"]
>;
export type TransactionChainStatus = NonNullable<
  components["schemas"]["Transaction"]["status"]
>;

/**
 * Confirmations threshold used only when neither Redemption.claimable nor
 * Project.finalityConfirmations is available.
 */
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
 * A `Funded` redemption is claimable on-chain as soon as it is funded, but the
 * indexer's view of the funding transaction is unconfirmed until it has the
 * configured number of confirmations. The server derives that as
 * `Redemption.claimable` (status is Funded and confirmations reach
 * Project.finalityConfirmations), which takes precedence. The
 * confirmations/threshold comparison below applies only when `claimable` is
 * absent.
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
 * Label for every status in the OpenAPI Transaction.status enum. It is keyed
 * by the generated `TransactionChainStatus` union, so adding an enum value
 * makes an incomplete map (and `TRANSACTION_TONE`) fail typecheck. For a
 * status outside the union, such as one a newer server adds, use
 * `transactionStatusLabel`, which falls back to "Unknown".
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

/** Label shown for any status the client doesn't recognize (forward-compat). */
export const TRANSACTION_STATUS_UNKNOWN_LABEL = "Unknown";

/**
 * Transaction states in which automated progress has stopped or is unsafe, so
 * a person needs to look: the attempt reverted on-chain (`reverted`) or was
 * never broadcast (`failed`), its block was dropped (`reorged`), whether it
 * reached the node is unknown and it must not be blindly resubmitted
 * (`broadcast_unknown`), another transaction consumed its nonce
 * (`nonce_consumed_externally`), or the transaction manager exhausted its
 * replacement attempts (`needs_intervention`). `pending`, `mined`, `confirmed`
 * and `replaced` are excluded.
 */
const TRANSACTION_ATTENTION: ReadonlySet<TransactionChainStatus> = new Set([
  "reverted",
  "reorged",
  "failed",
  "broadcast_unknown",
  "nonce_consumed_externally",
  "needs_intervention",
]);

/** Narrowing guard: is `status` one of the statuses this client has a label for? */
export function isKnownTransactionStatus(
  status: string,
): status is TransactionChainStatus {
  return Object.prototype.hasOwnProperty.call(TRANSACTION_STATUS_LABEL, status);
}

/**
 * Display label for any status string. A value missing from the generated enum,
 * such as one a newer server sends, gets "Unknown" instead of an empty label.
 */
export function transactionStatusLabel(status: string): string {
  return isKnownTransactionStatus(status)
    ? TRANSACTION_STATUS_LABEL[status]
    : TRANSACTION_STATUS_UNKNOWN_LABEL;
}

/**
 * True for the states in `TRANSACTION_ATTENTION`. An unrecognized status is
 * flagged too, since the client cannot tell whether it is safe. Transaction
 * lists can use this for a page-level warning instead of relying on a single
 * row's badge color.
 */
export function needsAttention(status: string): boolean {
  return !isKnownTransactionStatus(status) || TRANSACTION_ATTENTION.has(status);
}

/**
 * True when a Pending request's beneficiaryAllowed is false, indicating that
 * fundRedemption would fail compliance. Absent beneficiaryAllowed means false.
 * Funded claims do not recheck compliance, so later status changes do not
 * block their payout.
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
 * passes, but the registry's `isAllowed` returns false. Showing `Expired`
 * avoids implying that the wallet can still buy, transfer or redeem.
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
