package models

import (
	"strings"
	"time"
)

// EventDerivedTxIDPrefix marks a Transaction record that the stack-transactions
// projector (txindex.ReconcileStackTransactions) built from indexed chain events,
// as opposed to one the transaction manager signed and broadcast. Its ID is
// EventDerivedTxIDPrefix + txHash. It has no signed payload or nonce, and
// TxManager.RefreshStatuses ignores it because that only manages transactions it
// submitted.
const EventDerivedTxIDPrefix = "evt:"

// IsEventDerived reports whether t has the EventDerivedTxIDPrefix
func IsEventDerived(t *Transaction) bool {
	return strings.HasPrefix(t.ID, EventDerivedTxIDPrefix)
}

// TxStatus is the transaction manager's lifecycle state
type TxStatus string

const (
	TxPending   TxStatus = "pending"
	TxMined     TxStatus = "mined"
	TxConfirmed TxStatus = "confirmed"
	TxReplaced  TxStatus = "replaced"
	TxReverted  TxStatus = "reverted"
	// TxReorged means a transaction that had a receipt (Mined, Confirmed or Reverted,
	// including one past the confirmations threshold) no longer has one, or its
	// receipt's block is no longer canonical at that height. The Idempotency-Key of a
	// TxReorged record can be reused by a new Submit only if AllowReorgRetry was set,
	// because the transaction may return to the mempool. A new Submit then re-queries
	// the nonce and signs again.
	TxReorged TxStatus = "reorged"
	// TxFailed means signing succeeded and SendTransaction returned a definite error,
	// so nothing reached the mempool under the record's nonce. The Idempotency-Key is
	// retryable: TxManager.Submit treats a TxFailed record like no earlier record.
	TxFailed TxStatus = "failed"
	// TxBroadcastUnknown stores SignedRawTx but does not establish whether the node
	// accepted it. A transport error may lose the acknowledgement, or a crash may
	// interrupt broadcasting after persistence.
	//
	// Submit cannot retry the Idempotency-Key with a new nonce or signature because
	// that could duplicate the operation. RefreshStatuses resolves the known hash
	// and rebroadcasts the stored bytes while unresolved, until a receipt appears
	// or operator intervention is required.
	TxBroadcastUnknown TxStatus = "broadcast_unknown"
	// TxNonceConsumedExternally means the account's on-chain nonce moved past this
	// transaction's Nonce without a receipt for this transaction's hash. Another
	// transaction was mined at that nonce, for example one sent directly with the
	// signer's key outside TxManager or a replacement TxManager lost track of, so this
	// record cannot confirm. The Idempotency-Key is retryable only if AllowReorgRetry
	// was set, because the other transaction may have performed the same business
	// action.
	TxNonceConsumedExternally TxStatus = "nonce_consumed_externally"
	// TxNeedsIntervention means a stuck transaction used up its replacement attempts.
	// It is flagged for an operator so that later transactions are not reported
	// finalized behind it. TxManager.Replace acts only on Pending records, so it does
	// nothing further for this one. A new Submit under the same Idempotency-Key is not
	// allowed, because whether the nonce is still replaceable or was consumed is
	// unknown. While a signer has a TxNeedsIntervention transaction, Submit refuses new
	// submissions for that signer, since a transaction behind a stuck nonce would also
	// stick.
	TxNeedsIntervention TxStatus = "needs_intervention"
)

// AllTxStatuses lists every TxStatus the transaction manager can store and the API
// can return. The Transaction.status enum in api/openapi.yaml, the generated
// TypeScript type and the UI status maps must cover exactly this set, because the
// transactions handler returns the raw status string and a missing status would
// render as unknown. Tests in internal/api check the list against the constants
// above and against the OpenAPI enum.
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

// Transaction is a chain transaction, either submitted by the server or derived
// from chain events (collection: transactions)
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
	// SignedRawTx is the signed RLP transaction, 0x-prefixed hex, stored before
	// broadcast. RefreshStatuses rebroadcasts these bytes unchanged to resolve a
	// TxBroadcastUnknown record and never re-signs or changes the nonce. It is not
	// included in API responses (see dto.TransactionResponse).
	SignedRawTx string   `json:"-" bson:"signedRawTx,omitempty"`
	Status      TxStatus `json:"status" bson:"status"`
	BlockNumber uint64   `json:"blockNumber,omitempty" bson:"blockNumber,omitempty"`
	// BlockHash is the hash of the receipt's block when it was last observed.
	// RefreshStatuses compares it with the canonical header at BlockNumber to detect a
	// reorg.
	BlockHash  string `json:"-" bson:"blockHash,omitempty"`
	ReplacedBy string `json:"replacedBy,omitempty" bson:"replacedBy,omitempty"`
	// Replaces links a replacement record back to the transaction it replaces. With
	// ReplacedBy on the original, it lets RefreshStatuses repair a crash that stored
	// this record before marking the original Replaced.
	Replaces string `json:"-" bson:"replaces,omitempty"`
	// ReplacementCount is how many times the nonce has been replaced along the
	// Replaces chain: 0 for a record created by Submit, and the replaced record's count
	// plus one for a replacement. TxManager.Replace compares it with the configured cap
	// to decide between another fee bump and TxNeedsIntervention.
	ReplacementCount int `json:"replacementCount,omitempty" bson:"replacementCount,omitempty"`
	// FencingToken is the nonce lease's fencing token when the record was stored, in
	// "mongo-lease" TxCoordinationMode. It is zero in the default "in-process" mode.
	// It is kept for audit only. A superseded writer is rejected by a
	// NonceLeaseRepository.Renew call before the record is stored, not by checking this
	// field later.
	FencingToken uint64 `json:"-" bson:"fencingToken,omitempty"`
	// AllowReorgRetry is copied from SubmitRequest.AllowReorgRetry at Submit. It marks
	// the IdempotencyKey as reusable, with a new nonce, after TxReorged or
	// TxNonceConsumedExternally. The manager does not require every Kind to be
	// idempotent on-chain, so the caller must set it only for an action that is safe to
	// repeat, as compliance.StatusService does for setStatus. Without it, Submit
	// refuses the retry and RefreshStatuses blocks new submissions for that signer
	// until an operator identifies the canonical transaction at (chainId, sender,
	// nonce) and resolves the record, as for TxNeedsIntervention.
	AllowReorgRetry bool `json:"-" bson:"allowReorgRetry,omitempty"`
	// Version supports optimistic concurrency. TransactionRepository.UpdateConditional
	// increments it on each successful write and rejects a write whose expected version
	// no longer matches. This backs up NonceLeaseRepository.Renew at the storage layer
	// for state changes that could otherwise race it, such as the writes in
	// TxManager.Replace that mark the original Replaced or revert after a failure.
	Version int `json:"-" bson:"version,omitempty"`
	// Attempts lists the earlier signed attempts of this operation, so a nonce, hash
	// or signed payload is not lost when Submit retries a failed record under the same
	// IdempotencyKey. TxHash, SignedRawTx, Nonce and Status above describe the latest
	// attempt.
	Attempts    []TxAttempt `json:"-" bson:"attempts,omitempty"`
	SubmittedAt time.Time   `json:"submittedAt" bson:"submittedAt"`
	UpdatedAt   time.Time   `json:"updatedAt" bson:"updatedAt"`
}

// TxAttempt is a snapshot of one earlier sign-and-broadcast attempt of a
// Transaction, kept in Transaction.Attempts so the earlier hash, signed payload and
// outcome are not overwritten
type TxAttempt struct {
	Nonce  uint64 `json:"nonce" bson:"nonce"`
	TxHash string `json:"txHash" bson:"txHash"`
	// SignedRawTx is as in Transaction.SignedRawTx and is not included in API
	// responses
	SignedRawTx string    `json:"-" bson:"signedRawTx,omitempty"`
	Status      TxStatus  `json:"status" bson:"status"`
	RecordedAt  time.Time `json:"recordedAt" bson:"recordedAt"`
}
