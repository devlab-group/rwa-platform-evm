package models

import "time"

// ComplianceOperation is a submitted status-change transaction
// (collection: compliance_operations).
type ComplianceOperation struct {
	ID         string           `json:"id" bson:"_id"`
	Address    string           `json:"address" bson:"address"`
	Status     ComplianceStatus `json:"status" bson:"status"`
	ValidUntil int64            `json:"validUntil" bson:"validUntil"`
	TxHash     string           `json:"txHash" bson:"txHash"`
	Caller     string           `json:"caller" bson:"caller"`
	CreatedAt  time.Time        `json:"createdAt" bson:"createdAt"`
}
