package models

import "time"

// IncomePreparation stores createDistribution inputs and canonical snapshot bytes in
// income_preparations, retaining entries and Merkle proofs after creation
type IncomePreparation struct {
	ID            string `json:"preparationId" bson:"_id"`
	MerkleRoot    string `json:"merkleRoot" bson:"merkleRoot"` // 0x-hex
	Total         string `json:"total" bson:"total"`           // quote-token minimal units
	RecordBlock   uint64 `json:"recordBlock" bson:"recordBlock"`
	ClaimDeadline int64  `json:"claimDeadline" bson:"claimDeadline"`
	SnapshotURI   string `json:"snapshotURI" bson:"snapshotURI"`
	CID           string `json:"cid" bson:"cid"`
	// SnapshotSha256 is the 0x-hex SHA-256 of SnapshotRaw
	SnapshotSha256 string `json:"snapshotSha256" bson:"snapshotSha256"`
	EligibleSupply string `json:"eligibleSupply" bson:"eligibleSupply"`
	Distributed    string `json:"distributed" bson:"distributed"`
	HolderCount    int    `json:"holderCount" bson:"holderCount"`
	// SnapshotRaw is the RFC 8785 canonical snapshot document
	SnapshotRaw []byte    `json:"-" bson:"snapshotRaw"`
	CreatedAt   time.Time `json:"createdAt" bson:"createdAt"`
}
