package models

import "time"

// AuditLogEntry is an append-only operational audit record (collection: audit_logs).
type AuditLogEntry struct {
	ID        string         `json:"id" bson:"_id"`
	Category  string         `json:"category" bson:"category"`
	Actor     string         `json:"actor" bson:"actor"`
	Action    string         `json:"action" bson:"action"`
	Target    string         `json:"target" bson:"target"`
	Metadata  map[string]any `json:"metadata,omitempty" bson:"metadata,omitempty"`
	CreatedAt time.Time      `json:"createdAt" bson:"createdAt"`
}
