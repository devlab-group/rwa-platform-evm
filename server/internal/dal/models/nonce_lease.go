package models

import "time"

// NonceLease coordinates hot-key replicas under chainId+":"+signerAddress in
// nonce_leases. Acquire increases Token, including takeovers. Retain records without
// TTL deletion to prevent stale holder/token pairs from matching reused tokens; see
// NonceLeaseRepository.Acquire.
type NonceLease struct {
	Key       string    `json:"key" bson:"_id"`
	HolderID  string    `json:"holderId" bson:"holderId"`
	Token     uint64    `json:"token" bson:"token"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
}
