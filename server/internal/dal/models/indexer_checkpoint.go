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
	// ReconciliationRequired is set when a reorg's common ancestor cannot be verified
	// within the automatic recovery policy (see indexer.ErrReconciliationRequired).
	// While it is true, Poll does not advance, so read models built from chain_events
	// stay at a consistent point instead of continuing past an unresolved divergence.
	// Indexer.ResetToTrustedCheckpoint clears it.
	ReconciliationRequired bool `json:"reconciliationRequired,omitempty" bson:"reconciliationRequired,omitempty"`
	// Version counts atomic chunk commits (ChainChunkRepository.CommitChunk). A commit
	// applies only if the stored Version equals the one it was built on, so when
	// another indexer replica has advanced the checkpoint first, the commit writes
	// nothing. Plain Set writes (rollback, ResetToTrustedCheckpoint) reset it to 0,
	// and the next CommitChunk reads whatever Version it finds.
	Version int `json:"version,omitempty" bson:"version,omitempty"`
}
