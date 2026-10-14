package models

import "time"

// RecordStatus is the asset record lifecycle
type RecordStatus string

const (
	RecordStatusDraft    RecordStatus = "Draft"
	RecordStatusPending  RecordStatus = "Pending"
	RecordStatusSigned   RecordStatus = "Signed"
	RecordStatusMinted   RecordStatus = "Minted"
	RecordStatusRejected RecordStatus = "Rejected"
)

// Proof is an external document reference bound by SHA-256
type Proof struct {
	Type   string `json:"type" bson:"type"`
	SHA256 string `json:"sha256" bson:"sha256"`
	URI    string `json:"uri,omitempty" bson:"uri,omitempty"`
}

// AssetRecord is one tokenization record (collection: asset_records)
type AssetRecord struct {
	RecordID       string       `json:"recordId" bson:"_id"`
	ProjectID      string       `json:"projectId" bson:"projectId"`
	Status         RecordStatus `json:"status" bson:"status"`
	AssetRaw       []byte       `json:"-" bson:"assetRaw"`
	MetadataRaw    []byte       `json:"-" bson:"metadataRaw"` // full canonical metadata envelope, needed to rebuild the .rwa package
	Amount         string       `json:"amount" bson:"amount"`
	Unit           string       `json:"unit" bson:"unit"`
	Proofs         []Proof      `json:"proofs" bson:"proofs"`
	MetadataDigest string       `json:"metadataDigest" bson:"metadataDigest"`
	CID            string       `json:"cid" bson:"cid"`
	RecordKey      string       `json:"recordKey" bson:"recordKey"`
	Nonce          string       `json:"nonce" bson:"nonce"`
	ValidUntil     int64        `json:"validUntil" bson:"validUntil"`
	MintTxHash     string       `json:"mintTxHash,omitempty" bson:"mintTxHash,omitempty"`
	// Version is an optimistic-concurrency counter. A conditional update
	// (UpdateConditional) applies only if the stored Version equals the one the caller
	// read, and then increments it, so of two concurrent reissues only one succeeds.
	// It has no omitempty, so a new record stores version 0 explicitly. A missing field
	// would not match the {version: 0} filter of the first conditional update.
	Version   int       `json:"-" bson:"version"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
}
