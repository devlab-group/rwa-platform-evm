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
	// Version enables UpdateConditional to accept only the version read, then increment
	// it, admitting one concurrent reissue. Store initial 0 explicitly: an absent field
	// would not match MongoDB's {version: 0} filter.
	Version   int       `json:"-" bson:"version"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
}
