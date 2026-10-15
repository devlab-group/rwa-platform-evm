package models

import "time"

// AuditPackage stores the latest record's build metadata in audit_packages, not archive
// bytes. RecordService.BuildPackage rebuilds from stored inputs and the live auditor;
// identical inputs produce identical bytes. Each build replaces this row.
type AuditPackage struct {
	RecordID      string `json:"recordId" bson:"_id"`
	PackageSHA256 string `json:"packageSha256" bson:"packageSha256"`
	Size          int64  `json:"size" bson:"size"`
	// RecordVersion is the AssetRecord.Version the package was built from
	RecordVersion int `json:"recordVersion" bson:"recordVersion"`
	// CID is AssetRecord.CID at build time, identifying the attested metadata
	CID string `json:"cid" bson:"cid"`
	// Frozen is persisted but unused by package workflows; builds leave it false
	Frozen bool `json:"frozen" bson:"frozen"`
	// Auditor through Vault mirror MintAttestation fields in typed-data.json:
	// digests/addresses use 0x-hex; amounts use decimal strings
	Auditor         string    `json:"auditor" bson:"auditor"`
	ProfileDigest   string    `json:"profileDigest" bson:"profileDigest"`
	MetadataDigest  string    `json:"metadataDigest" bson:"metadataDigest"`
	RecordKey       string    `json:"recordKey" bson:"recordKey"`
	Amount          string    `json:"amount" bson:"amount"`
	Nonce           string    `json:"nonce" bson:"nonce"`
	ValidUntil      int64     `json:"validUntil" bson:"validUntil"`
	Vault           string    `json:"vault" bson:"vault"`
	TypedDataDigest string    `json:"typedDataDigest" bson:"typedDataDigest"`
	CreatedAt       time.Time `json:"createdAt" bson:"createdAt"`
}
