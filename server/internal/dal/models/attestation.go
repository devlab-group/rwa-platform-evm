package models

import "time"

// Attestation records a verified signed-result (collection: attestations).
type Attestation struct {
	RecordID        string    `json:"recordId" bson:"_id"`
	FormatVersion   string    `json:"formatVersion" bson:"formatVersion"`
	Auditor         string    `json:"auditor" bson:"auditor"`
	PrimaryType     string    `json:"primaryType" bson:"primaryType"`
	TypedDataDigest string    `json:"typedDataDigest" bson:"typedDataDigest"`
	Signature       string    `json:"signature" bson:"signature"`
	SignedAt        time.Time `json:"signedAt" bson:"signedAt"`
	Verified        bool      `json:"verified" bson:"verified"`
	TxHash          string    `json:"txHash,omitempty" bson:"txHash,omitempty"`
	CreatedAt       time.Time `json:"createdAt" bson:"createdAt"`
}
