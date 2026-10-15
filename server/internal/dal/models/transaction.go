package models

import (
	"strings"
	"time"
)

// EventDerivedTxIDPrefix identifies txindex.ReconcileStackTransactions records as
// prefix+txHash. They lack signed payloads and meaningful nonces;
// TxManager.RefreshStatuses skips them because the event projector owns their
// lifecycle.
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
	// TxReorged indicates a previously observed receipt disappeared, including after
	// confirmation, or a fetched receipt's block hash disagrees with the canonical header.
	// Same-key Submit requires AllowReorgRetry and re-queries the nonce before signing,
	// since the original may return to the mempool.
	TxReorged TxStatus = "reorged"
	// TxFailed marks a rejected or abandoned attempt. Submit may retry its IdempotencyKey,
	// retaining the record ID and archiving earlier signed attempts.
	TxFailed TxStatus = "failed"
	// TxBroadcastUnknown retains SignedRawTx after an uncertain send or
	// persistence-to-broadcast crash. Submit cannot allocate a new nonce/signature for the
	// key. RefreshStatuses checks the hash and rebroadcasts identical bytes until a
	// receipt or externally consumed nonce resolves it; operator intervention may be needed.
	TxBroadcastUnknown TxStatus = "broadcast_unknown"
	// TxNonceConsumedExternally means the mined account nonce passed this attempt without
	// its receipt, indicating an external or untracked replacement. Same-key retry
	// requires AllowReorgRetry because that transaction may have performed the operation.
	TxNonceConsumedExternally TxStatus = "nonce_consumed_externally"
	// TxNeedsIntervention marks exhausted replacement attempts. Replace accepts only
	// Pending, and Submit refuses both the same key and further submissions for this
	// signer until operator resolution: the nonce's outcome is unresolved and later nonces
	// may remain stuck.
	TxNeedsIntervention TxStatus = "needs_intervention"
)

// AllTxStatuses must match TxStatus constants, OpenAPI's Transaction.status, generated
// TypeScript and UI maps. The API returns raw status strings; missing UI entries render
// unknown. internal/api tests compare constants and OpenAPI with this list.
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
	// SignedRawTx stores 0x-hex transaction bytes before broadcast: an EIP-2718 envelope
	// or legacy RLP. RefreshStatuses rebroadcasts them without changing nonce or
	// signature. The API omits them; see dto.TransactionResponse.
	SignedRawTx string   `json:"-" bson:"signedRawTx,omitempty"`
	Status      TxStatus `json:"status" bson:"status"`
	BlockNumber uint64   `json:"blockNumber,omitempty" bson:"blockNumber,omitempty"`
	// BlockHash records the last observed receipt block. RefreshStatuses checks fetched
	// receipt hashes against canonical headers at BlockNumber.
	BlockHash  string `json:"-" bson:"blockHash,omitempty"`
	ReplacedBy string `json:"replacedBy,omitempty" bson:"replacedBy,omitempty"`
	// Replaces links a replacement to its original so RefreshStatuses can repair an
	// interrupted Replaced/ReplacedBy update
	Replaces string `json:"-" bson:"replaces,omitempty"`
	// ReplacementCount tracks attempts at this nonce, including rejected sends: Submit
	// starts at 0; replacements increment it. Replace compares it with the cap before
	// further fee bumps or TxNeedsIntervention.
	ReplacementCount int `json:"replacementCount,omitempty" bson:"replacementCount,omitempty"`
	// FencingToken records the mongo-lease token for audit; in-process mode stores 0.
	// Renew checks lease ownership before persistence; this field does not enforce the
	// write fence.
	FencingToken uint64 `json:"-" bson:"fencingToken,omitempty"`
	// AllowReorgRetry copies SubmitRequest's permission for same-key, new-nonce retries
	// after TxReorged or TxNonceConsumedExternally. Callers must opt in only for safely
	// repeatable actions, such as setStatus. Otherwise Submit blocks the signer until an
	// operator resolves the canonical (chainId, sender, nonce), as with
	// TxNeedsIntervention.
	AllowReorgRetry bool `json:"-" bson:"allowReorgRetry,omitempty"`
	// Version lets UpdateConditional reject stale writes and increment successful
	// versions. It supplements pre-persistence lease renewal for Replace's original-link
	// and failure-revert updates.
	Version int `json:"-" bson:"version,omitempty"`
	// Attempts preserves prior nonce, hash, signed bytes and outcome when Submit reuses an
	// IdempotencyKey's record. Top-level TxHash, SignedRawTx, Nonce and Status describe
	// the latest attempt.
	Attempts    []TxAttempt `json:"-" bson:"attempts,omitempty"`
	SubmittedAt time.Time   `json:"submittedAt" bson:"submittedAt"`
	UpdatedAt   time.Time   `json:"updatedAt" bson:"updatedAt"`
}

// TxAttempt preserves an earlier signed attempt in Transaction.Attempts, retaining its
// hash, payload and outcome
type TxAttempt struct {
	Nonce  uint64 `json:"nonce" bson:"nonce"`
	TxHash string `json:"txHash" bson:"txHash"`
	// SignedRawTx is as in Transaction.SignedRawTx and is not included in API responses
	SignedRawTx string    `json:"-" bson:"signedRawTx,omitempty"`
	Status      TxStatus  `json:"status" bson:"status"`
	RecordedAt  time.Time `json:"recordedAt" bson:"recordedAt"`
}
