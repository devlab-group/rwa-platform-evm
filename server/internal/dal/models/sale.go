package models

import "time"

// Purchase is a Vault.buy fill (collection: purchases)
type Purchase struct {
	ID          string    `json:"id" bson:"_id"`
	Buyer       string    `json:"buyer" bson:"buyer"`
	Recipient   string    `json:"recipient" bson:"recipient"`
	TokenAmount string    `json:"tokenAmount" bson:"tokenAmount"`
	QuoteAmount string    `json:"quoteAmount" bson:"quoteAmount"`
	TxHash      string    `json:"txHash" bson:"txHash"`
	BlockNumber uint64    `json:"blockNumber" bson:"blockNumber"`
	CreatedAt   time.Time `json:"createdAt" bson:"createdAt"`
	// Generation marks sales.Service.Reconcile's rebuild: upsert survivors with the new
	// generation, then delete stale rows. Each surviving ID stays readable as its old or
	// new value; Generation is omitted from API responses.
	Generation int64 `json:"-" bson:"generation"`
}
