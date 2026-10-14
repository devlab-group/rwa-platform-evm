package models

import "time"

// AuditPackage records the values a .rwa package was built from for a record
// (collection: audit_packages). The package bytes are not stored. RecordService.BuildPackage
// rebuilds them from the record and the current auditor, and auditpkg.BuildPackage
// returns identical bytes for identical inputs. Each build replaces this row, so it
// describes the latest build.
type AuditPackage struct {
	RecordID      string `json:"recordId" bson:"_id"`
	PackageSHA256 string `json:"packageSha256" bson:"packageSha256"`
	Size          int64  `json:"size" bson:"size"`
	// RecordVersion is the AssetRecord.Version the package was built from
	RecordVersion int `json:"recordVersion" bson:"recordVersion"`
	// CID is the content CID of the metadata the package attests to, equal to
	// AssetRecord.CID at build time
	CID string `json:"cid" bson:"cid"`
	// Frozen is stored but no code sets or reads it, so it is always false
	Frozen bool `json:"frozen" bson:"frozen"`
	// Auditor through Vault are the eip712.MintAttestation fields that BuildPackage
	// embeds in typed-data.json. Digests and addresses are 0x-prefixed hex, and
	// amounts are decimal strings, as in AssetRecord.
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
