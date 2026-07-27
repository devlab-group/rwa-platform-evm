package models

import "time"

// WalletChallenge is a one-time wallet-ownership proof challenge
// (collection: wallet_challenges).
type WalletChallenge struct {
	ID        string    `json:"id" bson:"_id"`
	Address   string    `json:"address" bson:"address"`
	Nonce     string    `json:"nonce" bson:"nonce"`
	Message   string    `json:"message" bson:"message"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
	Used      bool      `json:"used" bson:"used"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}
