package models

import "time"

// AdminChallenge stores an address-keyed, single-use admin login message in
// admin_challenges, separate from investor challenges and sessions. POST
// /auth/challenge returns Message for personal_sign; /auth/session verifies {address,
// signature} against it. Upsert replaces the normalized EIP-55 _id's challenge.
type AdminChallenge struct {
	Address   string    `json:"-" bson:"_id"` // normalized (EIP-55) admin-candidate address
	Message   string    `json:"message" bson:"message"`
	Nonce     string    `json:"nonce" bson:"nonce"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
	Used      bool      `json:"used" bson:"used"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}
