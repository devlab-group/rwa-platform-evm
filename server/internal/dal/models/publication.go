package models

import "time"

// PublicationState is the replication state of one published IPFS item. It is
// separate from local upload and pin success: a CID gives content addressing and
// integrity but says nothing about whether the content is available anywhere.
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
	// Pinned means the destination accepted an AddRaw or Pin call for the CID. That
	// is an API-level success and not proof that the content can be retrieved, which
	// Verified records.
	Pinned bool `json:"pinned" bson:"pinned"`
	// Verified means fetching the content from this destination and checking its
	// digest succeeded (see ipfs.ReplicationManager) at LastVerifiedAt. It is false
	// until a verification succeeds.
	Verified       bool      `json:"verified" bson:"verified"`
	LastVerifiedAt time.Time `json:"lastVerifiedAt,omitempty" bson:"lastVerifiedAt,omitempty"`
	LastError      string    `json:"lastError,omitempty" bson:"lastError,omitempty"`
}

// PublicationRecord tracks the replication of one published item across the local
// node and the backup destinations (collection: ipfs_publications). The caller
// chooses ID, for example an AssetRecord's RecordID, so it can look up publication
// status without knowing the CID.
type PublicationRecord struct {
	ID                   string              `json:"id" bson:"_id"`
	CID                  string              `json:"cid" bson:"cid"`
	State                PublicationState    `json:"state" bson:"state"`
	Destinations         []DestinationStatus `json:"destinations" bson:"destinations"`
	ReplicationThreshold int                 `json:"replicationThreshold" bson:"replicationThreshold"`
	CreatedAt            time.Time           `json:"createdAt" bson:"createdAt"`
	UpdatedAt            time.Time           `json:"updatedAt" bson:"updatedAt"`
}
