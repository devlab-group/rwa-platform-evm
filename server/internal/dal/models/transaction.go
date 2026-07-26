package models

import (
	"strings"
	"time"
)

// EventDerivedTxIDPrefix marks a Transaction record synthesized by the
// stack-transactions projector (txindex.ReconcileStackTransactions) from
// indexed chain events, rather than one the transaction manager itself signed
// and broadcast. Such a record's ID is EventDerivedTxIDPrefix + txHash. It
// carries no signed payload/nonce (those fields stay zero) and MUST be ignored
// by TxManager.RefreshStatuses, which only manages transactions it submitted.
const EventDerivedTxIDPrefix = "evt:"

// IsEventDerived reports whether t was synthesized from chain events by the
// stack-transactions projector (see EventDerivedTxIDPrefix), as opposed to a
// transaction-manager-submitted record.
func IsEventDerived(t *Transaction) bool {
	return strings.HasPrefix(t.ID, EventDerivedTxIDPrefix)
}

// TxStatus is the transaction-manager lifecycle state.
type TxStatus string

const (
	TxPending   TxStatus = "pending"
	TxMined     TxStatus = "mined"
	TxConfirmed TxStatus = "confirmed"
	TxReplaced  TxStatus = "replaced"
	TxReverted  TxStatus = "reverted"
	// TxReorged means a transaction that previously had a receipt (Mined,
	// Confirmed, or Reverted — including one that had already crossed the
	// configured confirmations threshold) no longer does, or its receipt's
	// block is no longer the canonical block at that height — even a
	// confirmed transaction is re-checked for reorgs. Like TxFailed,
	// a TxReorged record's Idempotency-Key is retryable — see TxFailed's
	// doc comment — because the chain no longer contains this attempt under
	// any nonce/hash TxManager can still track; a fresh Submit under the
	// same key re-queries the nonce and resubmits.
	TxReorged TxStatus = "reorged"
	// TxFailed means signing succeeded but SendTransaction itself returned
	// a DEFINITE (non-ambiguous) error — nothing reached the mempool under
	// this record's nonce. Unlike every other status except
	// TxReorged, a TxFailed record's Idempotency-Key is retryable:
	// TxManager.Submit treats it the same as "no prior record" instead of
	// replaying it as an already-submitted result.
	TxFailed TxStatus = "failed"
	// TxBroadcastUnknown means the record was durably persisted — including
	// the exact signed RLP bytes in SignedRawTx — but whether it actually
	// reached/was accepted by the node is UNKNOWN: either SendTransaction
	// itself returned a transport-level error (timeout, connection reset,
	// context deadline) that could just as easily mean "the node accepted
	// it and the ack was lost" as "it never arrived", or the
	// process crashed between persisting a replacement's intent and
	// actually broadcasting it. Unlike TxFailed/TxReorged, a
	// TxBroadcastUnknown record's Idempotency-Key is NOT retryable via a
	// fresh Submit — allocating a new nonce or re-signing while the
	// outcome is unknown risks a duplicate on-chain operation. It can only
	// be resolved by RefreshStatuses: query the chain by the known hash,
	// and if no receipt exists yet, rebroadcast the EXACT SAME persisted
	// bytes (never a different nonce/signature) until a receipt appears or
	// an operator intervenes.
	TxBroadcastUnknown TxStatus = "broadcast_unknown"
	// TxNonceConsumedExternally means the account's on-chain (latest/mined)
	// nonce has advanced past this transaction's Nonce without a receipt
	// ever being observed for THIS transaction's hash — an externally
	// consumed nonce we detect and reconcile. Some other
	// transaction — not this one, e.g. one submitted directly against the
	// signer outside TxManager, or replaced by a mechanism TxManager lost
	// track of — was mined at this nonce, so this record can never confirm.
	// Like TxFailed/TxReorged, its Idempotency-Key is retryable: the
	// business operation this record represents never actually executed
	// under it.
	TxNonceConsumedExternally TxStatus = "nonce_consumed_externally"
	// TxNeedsIntervention means a stuck transaction exhausted its
	// configured replacement-attempt limit: it must be flagged for operator
	// intervention so later nonce-dependent transactions aren't incorrectly
	// reported as finalized behind it. It is
	// terminal from TxManager's own automated-replacement perspective —
	// Replace refuses to act on it further — but NOT retryable via a fresh
	// Submit under the same Idempotency-Key, because whether the
	// underlying nonce is still live/replaceable or was already consumed
	// is unknown without operator investigation. While any transaction for
	// a signer is TxNeedsIntervention, Submit refuses new submissions for
	// that same signer (see TxManager's doc comment): a chain enforces
	// strict nonce ordering, so anything submitted behind a stuck nonce
	// would get stuck too.
	TxNeedsIntervention TxStatus = "needs_intervention"
)

// AllTxStatuses is the single source-of-truth list of every TxStatus the
// transaction manager can persist and the API can therefore emit. The
// OpenAPI Transaction.status enum, the generated TS type, and the
// UI status maps must cover exactly this set — the Go transactions handler
// returns the raw status string, so any status missing from the enum/UI
// renders as an undefined/unknown badge). A contract test
// (TestAllTxStatusesMatchesConstants) asserts this slice stays in lockstep
// with the const block above, and TestAllTxStatusesMatchesOpenAPIEnum asserts
// it matches api/openapi.yaml's enum exactly.
var AllTxStatuses = []TxStatus{
	TxPending,
	TxMined,
	TxConfirmed,
	TxReplaced,
	TxReverted,
	TxReorged,
	TxFailed,
	TxBroadcastUnknown,
	TxNonceConsumedExternally,
	TxNeedsIntervention,
}

// Transaction is one server-submitted chain transaction (collection: transactions).
type Transaction struct {
	ID             string `json:"id" bson:"_id"`
	IdempotencyKey string `json:"idempotencyKey,omitempty" bson:"idempotencyKey,omitempty"`
	Kind           string `json:"kind" bson:"kind"`
	ChainID        int64  `json:"chainId" bson:"chainId"`
	From           string `json:"from" bson:"from"`
	To             string `json:"to" bson:"to"`
	Data           string `json:"data" bson:"data"`
	Value          string `json:"value" bson:"value"`
	Nonce          uint64 `json:"nonce" bson:"nonce"`
	TxHash         string `json:"txHash" bson:"txHash"`
	// SignedRawTx is the exact signed RLP transaction bytes ("0x"-prefixed
	// hex), persisted BEFORE broadcast so it can always be replayed. It is
	// what RefreshStatuses
	// rebroadcasts, byte-for-byte, to resolve a TxBroadcastUnknown record —
	// never a re-signed/re-nonced transaction. Internal only: never
	// serialized in an API response (see the dedicated
	// dto.TransactionResponse view type).
	SignedRawTx string   `json:"-" bson:"signedRawTx,omitempty"`
	Status      TxStatus `json:"status" bson:"status"`
	BlockNumber uint64   `json:"blockNumber,omitempty" bson:"blockNumber,omitempty"`
	// BlockHash is the receipt's block hash at the time it was last
	// observed (the receipt's block HASH), used to detect
	// a reorg by comparing against the CURRENT canonical header at
	// BlockNumber on a later RefreshStatuses tick.
	BlockHash  string `json:"-" bson:"blockHash,omitempty"`
	ReplacedBy string `json:"replacedBy,omitempty" bson:"replacedBy,omitempty"`
	// Replaces is the reverse link set on a replacement record, back to the
	// original transaction it replaces, so an incomplete replacement can be
	// reconciled. Together with ReplacedBy on the original, this
	// lets RefreshStatuses self-heal a crash that persisted this record but
	// never got to mark the original Replaced/ReplacedBy.
	Replaces string `json:"-" bson:"replaces,omitempty"`
	// ReplacementCount is how many times this transaction's nonce has been
	// replaced so far, walking back through the Replaces chain (0 for an
	// original Submit-created record, N+1 for a record replacing one whose
	// own ReplacementCount was N). TxManager.Replace compares it against
	// the configured replacement-attempt cap to decide
	// whether to attempt another bumped-fee resubmission or mark the
	// transaction TxNeedsIntervention instead.
	ReplacementCount int `json:"replacementCount,omitempty" bson:"replacementCount,omitempty"`
	// FencingToken is the distributed nonce lease's fencing token in effect
	// when this record was persisted, in "mongo-lease" TxCoordinationMode
	// (multi-replica hot-key coordination). Zero in
	// "in-process" mode (the default) — see NonceLeaseRepository and
	// TxManager's doc comment. Recorded for observability/audit only; the
	// actual reject-a-superseded-writer enforcement happens live, via a
	// NonceLeaseRepository.Renew call gating the persist step itself (a
	// live repository check is race-free in a way comparing a locally
	// cached number never fully is), not by re-deriving trust from this
	// stored field later.
	FencingToken uint64 `json:"-" bson:"fencingToken,omitempty"`
	// AllowReorgRetry, set from SubmitRequest.AllowReorgRetry at Submit
	// time, marks this record's IdempotencyKey as safe to resubmit (same
	// key, fresh nonce) after TxReorged/TxNonceConsumedExternally. A reorged
	// transaction can still return to the mempool, and an externally consumed
	// nonce can represent an unknown replacement that performed the same
	// business action; the generic manager does not enforce that every Kind
	// is on-chain idempotent, so this stays opt-in. Only a caller that
	// KNOWS its own on-chain action is safely repeatable (e.g.
	// compliance.StatusService's setStatus — see its call site) should set
	// this; every other Kind defaults to false, so Submit refuses the
	// same-key retry and RefreshStatuses' signer-level guard blocks
	// further submissions for that signer until an operator identifies
	// the canonical transaction at (chainId, sender, nonce) and resolves
	// it — see TxNeedsIntervention's doc comment, which this now also
	// governs the entry into.
	AllowReorgRetry bool `json:"-" bson:"allowReorgRetry,omitempty"`
	// Version supports optimistic conditional writes: a caller acquires the
	// lock/lease before loading the record, then updates conditionally on
	// (id, expectedStatus, version, fencingToken), so a stale token is
	// rejected at the repository write itself rather than only in a prior
	// Renew call. TransactionRepository.UpdateConditional increments this on
	// every successful write and rejects a write whose caller-supplied
	// expectedVersion no longer matches the stored value — a second,
	// storage-level backstop independent of (and in addition to)
	// NonceLeaseRepository.Renew's application-level check, for the state
	// transitions that could otherwise race ahead of it (Replace's
	// original-Replaced / failure-revert writes — see TxManager.Replace).
	Version int `json:"-" bson:"version,omitempty"`
	// Attempts is an append-only forensic record of every distinct signed
	// payload/nonce/hash this logical operation has gone through BEFORE
	// its current one (see TxAttempt's doc comment). The
	// top-level TxHash/SignedRawTx/Nonce/Status fields above still reflect
	// the CURRENT/latest attempt (existing callers keep working
	// unchanged); this slice additionally retains every prior one instead
	// of losing it when Submit resubmits a TxFailed record under the same
	// IdempotencyKey/ID.
	Attempts    []TxAttempt `json:"-" bson:"attempts,omitempty"`
	SubmittedAt time.Time   `json:"submittedAt" bson:"submittedAt"`
	UpdatedAt   time.Time   `json:"updatedAt" bson:"updatedAt"`
}

// TxAttempt is one immutable, forensic snapshot of a single sign+broadcast
// attempt that a Transaction record's top-level fields are about to
// stop reflecting. One logical operation is modeled as an immutable record
// with append-only signed attempts: an earlier hash, signed payload,
// receipt, or replacement outcome is never overwritten. See
// Transaction.Attempts.
type TxAttempt struct {
	Nonce  uint64 `json:"nonce" bson:"nonce"`
	TxHash string `json:"txHash" bson:"txHash"`
	// SignedRawTx mirrors Transaction.SignedRawTx's doc comment: internal
	// only, never serialized in an API response.
	SignedRawTx string    `json:"-" bson:"signedRawTx,omitempty"`
	Status      TxStatus  `json:"status" bson:"status"`
	RecordedAt  time.Time `json:"recordedAt" bson:"recordedAt"`
}
