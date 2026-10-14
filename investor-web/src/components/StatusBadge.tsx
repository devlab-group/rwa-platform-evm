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

/** Tone for an unrecognized status: a visible warning rather than neutral */
const TRANSACTION_UNKNOWN_TONE = "badge--warning";

/** Redemption status badge, showing the display status derived by redemptionDisplayStatus. */
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
 * Shown next to the status badge when a Pending redemption's beneficiary is not
 * currently Allowed, so funding would revert. Renders nothing for any other
 * status (compliance no longer blocks it) or when `beneficiaryAllowed` is
 * absent from the response.
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
 * Transaction lifecycle badge for every status in the OpenAPI
 * Transaction.status enum. It takes a plain `string`, so a status outside the
 * generated union, such as one a newer server sends, renders an "Unknown"
 * badge with a warning tone and an attention marker instead of an empty label
 * or an undefined CSS class.
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
