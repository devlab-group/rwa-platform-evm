package models

import "time"

// Investor is a wallet's known status (collection: investors).
type Investor struct {
	Address           string           `json:"address" bson:"_id"`
	Status            ComplianceStatus `json:"status" bson:"status"`
	ValidUntil        int64            `json:"validUntil" bson:"validUntil"`
	OwnershipVerified bool             `json:"ownershipVerified" bson:"ownershipVerified"`
	CreatedAt         time.Time        `json:"createdAt" bson:"createdAt"`
	UpdatedAt         time.Time        `json:"updatedAt" bson:"updatedAt"`
}
