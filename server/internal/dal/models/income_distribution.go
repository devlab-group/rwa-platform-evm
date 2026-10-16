package models

// IncomeDistribution is one IncomeDistributor distribution derived from its indexed
// events in income_distributions. Generation marks the rebuild that wrote the row.
type IncomeDistribution struct {
	ID             string `json:"id" bson:"_id"` // decimal distribution id
	MerkleRoot     string `json:"merkleRoot" bson:"merkleRoot"`
	Total          string `json:"total" bson:"total"`
	RecordBlock    uint64 `json:"recordBlock" bson:"recordBlock"`
	ClaimDeadline  int64  `json:"claimDeadline" bson:"claimDeadline"` // follows ClaimDeadlineExtended
	SnapshotURI    string `json:"snapshotURI" bson:"snapshotURI"`
	SnapshotSha256 string `json:"snapshotSha256" bson:"snapshotSha256"`
	// Claimed sums IncomeClaimed amounts
	Claimed         string `json:"claimed" bson:"claimed"`
	Reclaimed       bool   `json:"reclaimed" bson:"reclaimed"`
	ReclaimedAmount string `json:"reclaimedAmount,omitempty" bson:"reclaimedAmount,omitempty"`
	CreateTxHash    string `json:"createTxHash" bson:"createTxHash"`
	// CreatedBlock and CreatedLogIndex order distributions as they were created
	CreatedBlock    uint64 `json:"createdBlock" bson:"createdBlock"`
	CreatedLogIndex uint   `json:"createdLogIndex" bson:"createdLogIndex"`
	Generation      int64  `json:"-" bson:"generation"`
}

// IncomeClaim is one IncomeClaimed event in income_claims, keyed by distribution and
// account so a wallet's claims can be listed
type IncomeClaim struct {
	ID             string `json:"id" bson:"_id"` // "<distributionId>:<lowercase account>"
	DistributionID string `json:"distributionId" bson:"distributionId"`
	Account        string `json:"account" bson:"account"` // lowercase 0x-hex
	Amount         string `json:"amount" bson:"amount"`
	TxHash         string `json:"txHash" bson:"txHash"`
	BlockNumber    uint64 `json:"blockNumber" bson:"blockNumber"`
	Generation     int64  `json:"-" bson:"generation"`
}
