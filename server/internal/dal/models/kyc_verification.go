package models

import "time"

// KYCVerification binds a KYC provider's own subject reference back to the
// wallet address it was started for (collection: kyc_verifications). It exists
// because providers differ in how a webhook identifies its subject: Sumsub
// echoes the externalUserId we set (the wallet address itself), so it needs no
// lookup — but Onfido's webhook carries only an applicant / workflow-run id and
// no custom field, so the address is resolved through this record, written when
// POST /compliance/kyc/start creates the provider-side verification. ID is
// KYCVerificationID(Provider, Ref); Ref is the provider reference the webhook
// carries (Onfido workflow-run id; for Sumsub, the externalUserId = address).
type KYCVerification struct {
	ID        string    `json:"id" bson:"_id"`
	Provider  string    `json:"provider" bson:"provider"`
	Ref       string    `json:"ref" bson:"ref"`
	Address   string    `json:"address" bson:"address"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}

// KYCVerificationID is the (provider,ref) technical key used as the
// kyc_verifications _id. Kept as one helper so the repository adapters and the
// handler that writes/reads these records all derive the same key.
func KYCVerificationID(provider, ref string) string { return provider + "|" + ref }
