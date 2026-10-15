package models

import "time"

// IndexerCheckpoint tracks the last scanned block per (chainId, address)
// (collection: indexer_checkpoints)
type IndexerCheckpoint struct {
	ChainID       int64     `json:"chainId" bson:"chainId"`
	Address       string    `json:"address" bson:"address"`
	LastBlock     uint64    `json:"lastBlock" bson:"lastBlock"`
	LastBlockHash string    `json:"lastBlockHash" bson:"lastBlockHash"`
	UpdatedAt     time.Time `json:"updatedAt" bson:"updatedAt"`
	// ReconciliationRequired pauses Poll after a reorg exceeds verified automatic
	// recovery. Events remain available without further scanning until
	// Indexer.ResetToTrustedCheckpoint clears the flag.
	ReconciliationRequired bool `json:"reconciliationRequired,omitempty" bson:"reconciliationRequired,omitempty"`
	// Version fences CommitChunk and RewindChunk: matching writes store expected+1; stale
	// writes change nothing. Plain Set stores the supplied version, so legacy non-atomic
	// rollback/reset paths can reset it to 0.
	Version int `json:"version,omitempty" bson:"version,omitempty"`
}
