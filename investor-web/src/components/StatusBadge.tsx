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

/** Tone for an unrecognized/forward-compat status: visible, not silently neutral. */
const TRANSACTION_UNKNOWN_TONE = "badge--warning";

/** Redemption status badge. Derives Pending/Funded/Claimable/Completed/Rejected/Cancelled. */
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
 * Shown alongside the status badge when a Pending redemption's beneficiary
 * has lost compliance eligibility, so funding would revert.
 * Renders nothing once the request is Funded/Completed/etc. (compliance no
 * longer blocks it) or if `beneficiaryAllowed` isn't present on the
 * response (defensive — degrades to no indicator rather than a guess).
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
      title="The recorded beneficiary is not currently Allowed. Funding will revert until compliance status is restored."
    >
      Blocked by compliance
    </span>
  );
}

/**
 * Transaction confirmation/lifecycle-state badge covering every
 * `domain.TxStatus` the server can emit. Accepts a plain `string` (not just
 * the generated union) so a forward-compat status a newer server sends renders
 * a clear "Unknown" badge with a visible tone and an `attention` marker,
 * rather than an empty label or an undefined CSS class.
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
          : `Unrecognized transaction status "${status}". This client build may be older than the server; treat it as needing operator review.`
      }
    >
      {transactionStatusLabel(status)}
    </span>
  );
}
