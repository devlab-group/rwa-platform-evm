package models

import "time"

// PublicationState is the durable-replication lifecycle of one published
// IPFS content item, kept SEPARATE from mere local upload/pin success.
// Publication and durable replication are distinct states: a CIDv1 gives
// content addressing and integrity, but says nothing about whether the
// content is actually available anywhere.
type PublicationState string

const (
	PublicationCreated            PublicationState = "Created"
	PublicationPinnedLocally      PublicationState = "PinnedLocally"
	PublicationReplicationPending PublicationState = "ReplicationPending"
	PublicationReplicated         PublicationState = "Replicated"
	PublicationReplicationFailed  PublicationState = "ReplicationFailed"
)

// DestinationStatus is one configured replication destination's last known
// state for a PublicationRecord.
type DestinationStatus struct {
	Name string `json:"name" bson:"name"`
	// Pinned means the destination accepted an AddRaw/Pin call for this
	// CID — an API-level success only, NOT proof the content is actually
	// retrievable — a successful HTTP/API response from a pinning provider
	// is not sufficient proof on its own. See Verified.
	Pinned bool `json:"pinned" bson:"pinned"`
	// Verified means a retrieval verification (fetch + digest match — see
	// ipfs.ReplicationManager.Verify) succeeded against this destination at
	// LastVerifiedAt. False until the first successful verification.
	Verified       bool      `json:"verified" bson:"verified"`
	LastVerifiedAt time.Time `json:"lastVerifiedAt,omitempty" bson:"lastVerifiedAt,omitempty"`
	LastError      string    `json:"lastError,omitempty" bson:"lastError,omitempty"`
}

// PublicationRecord tracks one published content item's replication status
// across a local node and configured backup destinations (collection:
// ipfs_publications). ID is caller-chosen
// (e.g. an AssetRecord's RecordID) so a caller can look up "is this
// package durably published" without needing to know its CID first.
type PublicationRecord struct {
	ID                   string              `json:"id" bson:"_id"`
	CID                  string              `json:"cid" bson:"cid"`
	State                PublicationState    `json:"state" bson:"state"`
	Destinations         []DestinationStatus `json:"destinations" bson:"destinations"`
	ReplicationThreshold int                 `json:"replicationThreshold" bson:"replicationThreshold"`
	CreatedAt            time.Time           `json:"createdAt" bson:"createdAt"`
	UpdatedAt            time.Time           `json:"updatedAt" bson:"updatedAt"`
}
