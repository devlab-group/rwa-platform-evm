package models

import "time"

// DeadLetterEntry is a log the indexer could not process
// (collection: indexer_dead_letters). The indexer stores such a log here and keeps
// scanning. The ID is deterministic, so a log that fails again on a later poll
// updates one entry (RetryCount, LastFailedAt) instead of adding a new one.
type DeadLetterEntry struct {
	ID          string `json:"id" bson:"_id"`
	ChainID     int64  `json:"chainId" bson:"chainId"`
	BlockNumber uint64 `json:"blockNumber" bson:"blockNumber"`
	BlockHash   string `json:"blockHash,omitempty" bson:"blockHash,omitempty"`
	TxHash      string `json:"txHash,omitempty" bson:"txHash,omitempty"`
	LogIndex    uint   `json:"logIndex" bson:"logIndex"`
	// EventSignature is the log's topic0 when it has one, the best available
	// indication of the event type, since decoding failed
	EventSignature string    `json:"eventSignature,omitempty" bson:"eventSignature,omitempty"`
	ErrorCategory  string    `json:"errorCategory" bson:"errorCategory"`
	ErrorMessage   string    `json:"errorMessage" bson:"errorMessage"`
	RetryCount     int       `json:"retryCount" bson:"retryCount"`
	FirstFailedAt  time.Time `json:"firstFailedAt" bson:"firstFailedAt"`
	LastFailedAt   time.Time `json:"lastFailedAt" bson:"lastFailedAt"`
	// SourceData is the JSON-encoded log (types.Log), kept so a retry can decode it
	// again without querying the chain and so the failing bytes can be inspected. It
	// is not re-read from the chain, where a reorg could replace it with a different
	// log.
	SourceData []byte `json:"-" bson:"sourceData"`
	// Resolved is set when an operator retry succeeds or the entry is dismissed. The
	// entry is kept as a record of how the event was handled.
	Resolved bool `json:"resolved" bson:"resolved"`
}

// DLQErrorCategory values say why a DeadLetterEntry exists
const (
	DLQErrorDecode = "decode"
	// DLQErrorOrphaned marks an entry whose log is no longer on the canonical chain.
	// Before a retry, Indexer.RetryDeadLetter requires the canonical header hash at the
	// log's block to equal the log's BlockHash and otherwise dismisses the entry
	// without ingesting it. Only RetryDeadLetter sets it.
	DLQErrorOrphaned = "orphaned"
)
