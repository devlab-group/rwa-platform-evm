package models

import "time"

// KYCVerification binds a KYC provider's reference to the wallet address the
// verification was started for (collection: kyc_verifications). Providers identify
// the subject of a webhook differently. Sumsub echoes the externalUserId that the
// server set, which is the wallet address, so it needs no lookup. Onfido's webhook
// carries only a workflow-run id, so POST /compliance/kyc/start stores this record
// and the webhook handler resolves the address through it. ID is
// KYCVerificationID(Provider, Ref), and Ref is the reference the webhook carries.
type KYCVerification struct {
	ID        string    `json:"id" bson:"_id"`
	Provider  string    `json:"provider" bson:"provider"`
	Ref       string    `json:"ref" bson:"ref"`
	Address   string    `json:"address" bson:"address"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}

// KYCVerificationID returns the _id of a kyc_verifications record for (provider,
// ref). Repository adapters and handlers share it so they derive the same key.
func KYCVerificationID(provider, ref string) string { return provider + "|" + ref }
