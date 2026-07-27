package models

import "time"

// AssetProfile is the operator-defined, immutable-per-deployment profile
// (collection: asset_profiles).
type AssetProfile struct {
	ProjectID     string    `json:"projectId" bson:"_id"`
	ProfileRaw    []byte    `json:"-" bson:"profileRaw"`
	Digest        string    `json:"digest" bson:"digest"`
	CID           string    `json:"cid" bson:"cid"`
	TokenDecimals uint8     `json:"tokenDecimals" bson:"tokenDecimals"`
	TokenUnit     string    `json:"tokenUnit" bson:"tokenUnit"`
	RecordIDLabel string    `json:"recordIdLabel" bson:"recordIdLabel"`
	CreatedAt     time.Time `json:"createdAt" bson:"createdAt"`
}
