// Labels chain statuses and splits Funded into confirming/Claimable for display
import type { components } from "./api-types";

export type RedemptionChainStatus = NonNullable<
  components["schemas"]["Redemption"]["status"]
>;
export type TransactionChainStatus = NonNullable<
  components["schemas"]["Transaction"]["status"]
>;

/** Fallback when neither claimable nor finalityConfirmations is supplied */
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
 * Funded claims have no on-chain confirmation delay, but the UI waits for indexed finality.
 * Prefer server claimable; otherwise compare confirmations with the project threshold or fallback.
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

/** Unknown-status label for forward compatibility */
export const TRANSACTION_STATUS_UNKNOWN_LABEL = "Unknown";

/**
 * States needing attention: reverted on-chain, failed before broadcast, reorged,
 * broadcast outcome unknown, nonce consumed externally or replacement attempts exhausted.
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

/** Narrows status to a known label key */
export function isKnownTransactionStatus(
  status: string,
): status is TransactionChainStatus {
  return Object.prototype.hasOwnProperty.call(TRANSACTION_STATUS_LABEL, status);
}

/** Returns a known label or Unknown for unsupported server statuses */
export function transactionStatusLabel(status: string): string {
  return isKnownTransactionStatus(status)
    ? TRANSACTION_STATUS_LABEL[status]
    : TRANSACTION_STATUS_UNKNOWN_LABEL;
}

/** Flags TRANSACTION_ATTENTION states and unknown statuses for badges or page-level warnings */
export function needsAttention(status: string): boolean {
  return !isKnownTransactionStatus(status) || TRANSACTION_ATTENTION.has(status);
}

/**
 * True only for Pending with beneficiaryAllowed === false; missing eligibility is not flagged.
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
 * Displays Expired when an Allowed record's nonzero validUntil has passed;
 * the registry retains Allowed in storage but isAllowed returns false
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
