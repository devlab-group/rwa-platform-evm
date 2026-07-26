package models

import "time"

// DeadLetterEntry is a block/tx/log the indexer could not process
// (collection: indexer_dead_letters). The indexer routes any block,
// transaction, or log it can't process successfully into this persistent
// failure queue instead of dropping or blocking on it. Its ID is
// deterministic — see indexer.Indexer's dead-letter key — so a repeated
// failure of the SAME log across polls updates one entry (RetryCount,
// LastFailedAt) instead of growing the queue unboundedly.
type DeadLetterEntry struct {
	ID          string `json:"id" bson:"_id"`
	ChainID     int64  `json:"chainId" bson:"chainId"`
	BlockNumber uint64 `json:"blockNumber" bson:"blockNumber"`
	BlockHash   string `json:"blockHash,omitempty" bson:"blockHash,omitempty"`
	TxHash      string `json:"txHash,omitempty" bson:"txHash,omitempty"`
	LogIndex    uint   `json:"logIndex" bson:"logIndex"`
	// EventSignature is the log's topic0, when present — the best available
	// "what kind of event was this" without a full ABI decode (which is
	// exactly what failed).
	EventSignature string    `json:"eventSignature,omitempty" bson:"eventSignature,omitempty"`
	ErrorCategory  string    `json:"errorCategory" bson:"errorCategory"`
	ErrorMessage   string    `json:"errorMessage" bson:"errorMessage"`
	RetryCount     int       `json:"retryCount" bson:"retryCount"`
	FirstFailedAt  time.Time `json:"firstFailedAt" bson:"firstFailedAt"`
	LastFailedAt   time.Time `json:"lastFailedAt" bson:"lastFailedAt"`
	// SourceData is the JSON-encoded raw log (types.Log's RPC wire
	// representation), retained so an operator's retry can re-attempt
	// decoding without re-querying the chain — and so the exact bytes that
	// failed are auditable. Never re-derived from live chain state: a deep
	// enough reorg could otherwise make a retry silently decode a
	// DIFFERENT log than the one that originally failed.
	SourceData []byte `json:"-" bson:"sourceData"`
	// Resolved is set once an operator's retry succeeds (or manually
	// dismisses the entry) — kept rather than deleted, as a durable record
	// that this canonical event was, in fact, eventually handled and how.
	Resolved bool `json:"resolved" bson:"resolved"`
}

// DLQErrorCategory values classify why a DeadLetterEntry exists.
const (
	DLQErrorDecode = "decode"
	// DLQErrorOrphaned marks an entry whose retained log is no longer part
	// of the canonical chain by the time an operator retried it. Before a DLQ
	// replay, RetryDeadLetter fetches the canonical header and requires
	// header.Hash == retainedLog.BlockHash; an orphan is resolved as
	// dismissed rather than ingested. Set by
	// indexer.Indexer.RetryDeadLetter, never by normal ingestion.
	DLQErrorOrphaned = "orphaned"
)
