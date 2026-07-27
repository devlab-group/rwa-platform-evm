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
