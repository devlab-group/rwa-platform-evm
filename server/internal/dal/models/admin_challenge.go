package models

import "time"

// AdminChallenge is a single-use, time-limited admin wallet-login challenge
// (collection: admin_challenges). Admin auth is stateless-JWT based: the SPA
// requests a challenge (POST /auth/challenge), personal_signs the returned
// Message, then submits {address, signature} (POST /auth/session) with NO
// client-echoed nonce — so the server must look the challenge back up by
// address and recompute Message to recover the signer. There is therefore
// exactly ONE active challenge per address: Upsert replaces any prior one,
// keyed by the normalized (EIP-55) address as _id.
//
// It is deliberately separate from both the investor WalletChallenge (which
// records an Investor ownership flag) and any bearer-session store: this
// carries no credential, only the message a valid admin signature must match.
type AdminChallenge struct {
	Address   string    `json:"-" bson:"_id"` // normalized (EIP-55) admin-candidate address
	Message   string    `json:"message" bson:"message"`
	Nonce     string    `json:"nonce" bson:"nonce"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
	Used      bool      `json:"used" bson:"used"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}
