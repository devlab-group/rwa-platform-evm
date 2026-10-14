package models

import "time"

// NonceLease is a distributed lease over one signer's nonce sequence
// (collection: nonce_leases). Replicas use it to share a hot key. Key is
// chainId+":"+signerAddress. Token is a fencing token that increases on every
// successful Acquire, whether a first grant or a takeover (see
// NonceLeaseRepository.Acquire). A TTL index must not remove these documents,
// because a deleted document would reset Token and break the guarantee that it only
// increases. The collection stays small, with one document per hot key.
type NonceLease struct {
	Key       string    `json:"key" bson:"_id"`
	HolderID  string    `json:"holderId" bson:"holderId"`
	Token     uint64    `json:"token" bson:"token"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
}
