package models

import "time"

// KYCVerification binds provider references to wallets in kyc_verifications, keyed by
// KYCVerificationID(Provider, Ref). Onfido webhooks carry a workflow-run ID, so the API
// stores and resolves this binding. Sumsub carries the wallet in externalUserId and
// needs no lookup.
type KYCVerification struct {
	ID        string    `json:"id" bson:"_id"`
	Provider  string    `json:"provider" bson:"provider"`
	Ref       string    `json:"ref" bson:"ref"`
	Address   string    `json:"address" bson:"address"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}

// KYCVerificationID derives the shared repository/handler key from provider and ref
func KYCVerificationID(provider, ref string) string { return provider + "|" + ref }
