package models

import "time"

// AdminChallenge stores a nonce-keyed, single-use personal_sign message in admin_challenges,
// separate from investor challenges/sessions. POST /auth/challenge returns Message; /auth/session
// verifies {address, nonce, signature}. See auth.AdminChallengeService for issuance and use.
type AdminChallenge struct {
	Nonce     string    `json:"nonce" bson:"_id"`
	Address   string    `json:"address" bson:"address"` // normalized (EIP-55) admin-candidate address
	Message   string    `json:"message" bson:"message"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
	Used      bool      `json:"used" bson:"used"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}
