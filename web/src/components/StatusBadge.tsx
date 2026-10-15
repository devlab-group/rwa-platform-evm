import {
  REDEMPTION_STATUS_LABEL,
  isKnownTransactionStatus,
  needsAttention,
  redemptionBlockedByCompliance,
  redemptionDisplayStatus,
  transactionStatusLabel,
  type RedemptionChainStatus,
  type RedemptionDisplayStatus,
  type TransactionChainStatus,
} from "../lib/status";

const REDEMPTION_TONE: Record<RedemptionDisplayStatus, string> = {
  Pending: "badge--neutral",
  Funded: "badge--info",
  Claimable: "badge--success",
  Completed: "badge--success",
  Rejected: "badge--danger",
  Cancelled: "badge--danger",
  Unknown: "badge--neutral",
};

const TRANSACTION_TONE: Record<TransactionChainStatus, string> = {
  pending: "badge--neutral",
  mined: "badge--info",
  confirmed: "badge--success",
  replaced: "badge--warning",
  reverted: "badge--danger",
  reorged: "badge--warning",
  failed: "badge--danger",
  broadcast_unknown: "badge--warning",
  nonce_consumed_externally: "badge--warning",
  needs_intervention: "badge--danger",
};

// Warn on unrecognized statuses
const TRANSACTION_UNKNOWN_TONE = "badge--warning";

/** Displays redemptionDisplayStatus, including Unknown for unsupported statuses */
export function RedemptionStatusBadge({
  status,
  claimable,
  confirmations,
  finalityConfirmations,
}: {
  status: RedemptionChainStatus | undefined;
  claimable?: boolean;
  confirmations?: number;
  finalityConfirmations?: number;
}) {
  const display = redemptionDisplayStatus(status, {
    claimable,
    confirmations,
    finalityConfirmations,
  });
  return (
    <span className={`badge ${REDEMPTION_TONE[display]}`} data-status={display}>
      {REDEMPTION_STATUS_LABEL[display]}
    </span>
  );
}

/**
 * Warns for Pending with beneficiaryAllowed === false; hidden for other states or absent
 * eligibility
 */
export function RedemptionComplianceBadge({
  status,
  beneficiaryAllowed,
}: {
  status: RedemptionChainStatus | undefined;
  beneficiaryAllowed?: boolean;
}) {
  if (!redemptionBlockedByCompliance(status, beneficiaryAllowed)) return null;
  return (
    <span
      className="badge badge--danger"
      data-status="compliance-blocked"
      title="The beneficiary is not Allowed. Restore compliance before funding or the call will revert."
    >
      Blocked by compliance
    </span>
  );
}

/**
 * Displays known transaction statuses; unsupported strings get an Unknown badge with
 * warning tone and attention marker
 */
export function TransactionStatusBadge({ status }: { status: string }) {
  const known = isKnownTransactionStatus(status);
  const tone = known ? TRANSACTION_TONE[status] : TRANSACTION_UNKNOWN_TONE;
  return (
    <span
      className={`badge ${tone}`}
      data-status={known ? status : "unknown"}
      data-attention={needsAttention(status) ? "true" : undefined}
      title={
        known
          ? undefined
          : `Unrecognized transaction status "${status}". The server may be newer than this client; operator review is required.`
      }
    >
      {transactionStatusLabel(status)}
    </span>
  );
}
