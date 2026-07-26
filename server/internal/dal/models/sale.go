package models

import "time"

// Purchase is a Vault.buy fill (collection: purchases).
type Purchase struct {
	ID          string    `json:"id" bson:"_id"`
	Buyer       string    `json:"buyer" bson:"buyer"`
	Recipient   string    `json:"recipient" bson:"recipient"`
	TokenAmount string    `json:"tokenAmount" bson:"tokenAmount"`
	QuoteAmount string    `json:"quoteAmount" bson:"quoteAmount"`
	TxHash      string    `json:"txHash" bson:"txHash"`
	BlockNumber uint64    `json:"blockNumber" bson:"blockNumber"`
	CreatedAt   time.Time `json:"createdAt" bson:"createdAt"`
	// Generation is internal read-model-rebuild bookkeeping,
	// never part of the public API response: sales.Service.Reconcile
	// stamps every record it upserts with the current rebuild's
	// generation, then deletes whatever is left with a DIFFERENT
	// generation — so a concurrent reader only ever sees the previous
	// generation's value for a not-yet-touched id, or the new generation's
	// value for an already-touched one, never a gap.
	Generation int64 `json:"-" bson:"generation"`
}
