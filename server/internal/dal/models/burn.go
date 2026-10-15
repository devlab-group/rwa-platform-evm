package models

import "time"

// BurnStatus is a burn's execution state
type BurnStatus string

const (
	BurnStatusPending BurnStatus = "Pending"
	BurnStatusBurned  BurnStatus = "Burned"
)

// Burn stores a retirement against a Minted record in the burns collection
type Burn struct {
	BurnID         string     `json:"burnId" bson:"_id"`
	RecordID       string     `json:"recordId" bson:"recordId"`
	ProjectID      string     `json:"projectId" bson:"projectId"`
	Status         BurnStatus `json:"status" bson:"status"`
	OperationID    string     `json:"operationId" bson:"operationId"` // keccak256(utf8(burnId)), 0x-hex
	Amount         string     `json:"amount" bson:"amount"`
	Unit           string     `json:"unit" bson:"unit"`
	MetadataRaw    []byte     `json:"-" bson:"metadataRaw"` // canonical metadata for rebuilding the .rwa package
	MetadataDigest string     `json:"metadataDigest" bson:"metadataDigest"`
	CID            string     `json:"cid" bson:"cid"`
	Nonce          string     `json:"nonce" bson:"nonce"`
	ValidUntil     int64      `json:"validUntil" bson:"validUntil"`
	BurnTxHash     string     `json:"burnTxHash,omitempty" bson:"burnTxHash,omitempty"`
	// Persist initial zero for UpdateConditional's version check
	Version   int       `json:"-" bson:"version"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
}
