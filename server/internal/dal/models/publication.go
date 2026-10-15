package models

import "time"

// PublicationState tracks replication separately from local upload/pinning. A CID
// establishes content identity and integrity, not availability.
type PublicationState string

const (
	PublicationCreated            PublicationState = "Created"
	PublicationPinnedLocally      PublicationState = "PinnedLocally"
	PublicationReplicationPending PublicationState = "ReplicationPending"
	PublicationReplicated         PublicationState = "Replicated"
	PublicationReplicationFailed  PublicationState = "ReplicationFailed"
)

// DestinationStatus is the last known state of one replication destination for a
// PublicationRecord
type DestinationStatus struct {
	Name string `json:"name" bson:"name"`
	// Pinned means AddRaw or Pin succeeded at the destination. Verified separately records
	// successful retrieval and digest checking.
	Pinned bool `json:"pinned" bson:"pinned"`
	// Verified records successful destination retrieval and digest checking at
	// LastVerifiedAt; it starts false
	Verified       bool      `json:"verified" bson:"verified"`
	LastVerifiedAt time.Time `json:"lastVerifiedAt,omitempty" bson:"lastVerifiedAt,omitempty"`
	LastError      string    `json:"lastError,omitempty" bson:"lastError,omitempty"`
}

// PublicationRecord tracks local and backup replication in ipfs_publications.
// Caller-chosen IDs, such as AssetRecord.RecordID, allow status lookup without the CID.
type PublicationRecord struct {
	ID                   string              `json:"id" bson:"_id"`
	CID                  string              `json:"cid" bson:"cid"`
	State                PublicationState    `json:"state" bson:"state"`
	Destinations         []DestinationStatus `json:"destinations" bson:"destinations"`
	ReplicationThreshold int                 `json:"replicationThreshold" bson:"replicationThreshold"`
	CreatedAt            time.Time           `json:"createdAt" bson:"createdAt"`
	UpdatedAt            time.Time           `json:"updatedAt" bson:"updatedAt"`
}
