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
	// Generation is read-model rebuild bookkeeping and is not part of any API
	// response. sales.Service.Reconcile stamps every record it upserts with the current
	// generation and then deletes records with a different one. A concurrent reader
	// therefore sees, for each id, either the previous generation's value or the new
	// one, and never a gap.
	Generation int64 `json:"-" bson:"generation"`
}
