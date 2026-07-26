package models

import "time"

// ChainEvent is one indexed log, identified by (chainId, address, txHash,
// logIndex) (collection: chain_events).
type ChainEvent struct {
	ChainID     int64          `json:"chainId" bson:"chainId"`
	Address     string         `json:"address" bson:"address"`
	TxHash      string         `json:"txHash" bson:"txHash"`
	LogIndex    uint           `json:"logIndex" bson:"logIndex"`
	BlockNumber uint64         `json:"blockNumber" bson:"blockNumber"`
	BlockHash   string         `json:"blockHash" bson:"blockHash"`
	Name        string         `json:"name" bson:"name"`
	Data        map[string]any `json:"data" bson:"data"`
	Removed     bool           `json:"removed" bson:"removed"`
	IndexedAt   time.Time      `json:"indexedAt" bson:"indexedAt"`
}

// EventKey uniquely identifies a chain event for idempotent ingestion.
type EventKey struct {
	ChainID  int64
	Address  string
	TxHash   string
	LogIndex uint
}

// IndexerCheckpoint tracks the last scanned block per (chainId, address)
// (collection: indexer_checkpoints).
type IndexerCheckpoint struct {
	ChainID       int64     `json:"chainId" bson:"chainId"`
	Address       string    `json:"address" bson:"address"`
	LastBlock     uint64    `json:"lastBlock" bson:"lastBlock"`
	LastBlockHash string    `json:"lastBlockHash" bson:"lastBlockHash"`
	UpdatedAt     time.Time `json:"updatedAt" bson:"updatedAt"`
	// ReconciliationRequired is set by the indexer when a
	// detected reorg's common ancestor can't be verified within the
	// configured automatic-recovery policy (see
	// indexer.ErrReconciliationRequired). While true, Poll refuses to
	// advance — every derived read model that depends only on persisted
	// chain_events is frozen at a consistent, if stale, point rather than
	// silently continuing past an unresolved divergence. Cleared by
	// indexer.Indexer.ResetToTrustedCheckpoint.
	ReconciliationRequired bool `json:"reconciliationRequired,omitempty" bson:"reconciliationRequired,omitempty"`
	// Version is a monotonic counter bumped on every atomic chunk commit
	// (repository.ChainChunkRepository.CommitChunk). It is the
	// fencing token that makes the checkpoint advance conditional: a commit
	// layered on an expected version that no longer matches the stored one
	// (another indexer replica advanced it first) is rejected without writing
	// anything, so two active indexers can never interleave a stale
	// checkpoint over a newer one. Plain Set writes (rollback,
	// ResetToTrustedCheckpoint) reset it to 0, which is fine: those are
	// operator/reorg re-baselines after which the next CommitChunk simply
	// reads and layers on whatever version it finds.
	Version int `json:"version,omitempty" bson:"version,omitempty"`
}
