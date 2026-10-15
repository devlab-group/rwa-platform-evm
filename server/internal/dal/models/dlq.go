package models

import "time"

// DeadLetterEntry stores an unprocessed log in indexer_dead_letters so scanning can
// continue. Deterministic IDs consolidate repeated failures, updating RetryCount and
// LastFailedAt rather than adding rows.
type DeadLetterEntry struct {
	ID          string `json:"id" bson:"_id"`
	ChainID     int64  `json:"chainId" bson:"chainId"`
	BlockNumber uint64 `json:"blockNumber" bson:"blockNumber"`
	BlockHash   string `json:"blockHash,omitempty" bson:"blockHash,omitempty"`
	TxHash      string `json:"txHash,omitempty" bson:"txHash,omitempty"`
	LogIndex    uint   `json:"logIndex" bson:"logIndex"`
	// EventSignature holds topic0 when available, identifying the event despite decode
	// failure
	EventSignature string    `json:"eventSignature,omitempty" bson:"eventSignature,omitempty"`
	ErrorCategory  string    `json:"errorCategory" bson:"errorCategory"`
	ErrorMessage   string    `json:"errorMessage" bson:"errorMessage"`
	RetryCount     int       `json:"retryCount" bson:"retryCount"`
	FirstFailedAt  time.Time `json:"firstFailedAt" bson:"firstFailedAt"`
	LastFailedAt   time.Time `json:"lastFailedAt" bson:"lastFailedAt"`
	// SourceData retains JSON-encoded types.Log bytes for inspection and retry without
	// refetching a log that a reorg may replace
	SourceData []byte `json:"-" bson:"sourceData"`
	// Resolved records successful operator retry or dismissal; retain the entry as
	// handling history
	Resolved bool `json:"resolved" bson:"resolved"`
}

// DLQErrorCategory values say why a DeadLetterEntry exists
const (
	DLQErrorDecode = "decode"
	// DLQErrorOrphaned is set only by Indexer.RetryDeadLetter when the retained BlockHash
	// differs from the canonical header. The entry is dismissed without ingestion.
	DLQErrorOrphaned = "orphaned"
)
