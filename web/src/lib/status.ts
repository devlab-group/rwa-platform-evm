// Labels chain states and splits Funded into confirming/Claimable for display
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
 * Funded claims have no on-chain confirmation delay; UI finality uses server claimable
 * first, otherwise confirmations against the project threshold or fallback
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
 * Typed transaction labels; new statuses also require TRANSACTION_TONE updates.
 * Use transactionStatusLabel for arbitrary strings.
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

/*
 * States needing attention: reverted on-chain, definite pre-mempool rejection, reorged,
 * unknown broadcast outcome, externally consumed nonce or exhausted replacements.
 * Pending, mined, confirmed and replaced are excluded; see TRANSACTION_STATUS_LABEL.
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

/** Flags TRANSACTION_ATTENTION states and unknown statuses for page-level warnings */
export function needsAttention(status: string): boolean {
  return !isKnownTransactionStatus(status) || TRANSACTION_ATTENTION.has(status);
}

/**
 * Flags Pending with beneficiaryAllowed === false; missing eligibility is not flagged.
 * Funded claims skip beneficiary platform-compliance checks.
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
 * Displays Expired when an Allowed record has passed nonzero validUntil; stored status remains
 * Allowed
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
