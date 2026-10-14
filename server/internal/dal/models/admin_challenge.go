package models

import "time"

// AdminChallenge is a single-use admin wallet-login challenge
// (collection: admin_challenges). The admin SPA requests a challenge
// (POST /auth/challenge), signs the returned Message with personal_sign, and
// submits {address, signature} (POST /auth/session). The request carries no
// nonce, so the server finds the challenge by address and recomputes Message to
// recover the signer. Each address therefore has one active challenge: Upsert
// replaces any earlier one, and the normalized (EIP-55) address is the _id.
//
// It is separate from the investor WalletChallenge and from sessions. It holds no
// credential, only the message that a valid admin signature must cover.
type AdminChallenge struct {
	Address   string    `json:"-" bson:"_id"` // normalized (EIP-55) admin-candidate address
	Message   string    `json:"message" bson:"message"`
	Nonce     string    `json:"nonce" bson:"nonce"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
	Used      bool      `json:"used" bson:"used"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}
