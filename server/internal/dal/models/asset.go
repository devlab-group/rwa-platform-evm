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

// RecordStatus is the asset record lifecycle.
type RecordStatus string

const (
	RecordStatusDraft    RecordStatus = "Draft"
	RecordStatusPending  RecordStatus = "Pending"
	RecordStatusSigned   RecordStatus = "Signed"
	RecordStatusMinted   RecordStatus = "Minted"
	RecordStatusRejected RecordStatus = "Rejected"
)

// Proof is an external document reference bound by SHA-256.
type Proof struct {
	Type   string `json:"type" bson:"type"`
	SHA256 string `json:"sha256" bson:"sha256"`
	URI    string `json:"uri,omitempty" bson:"uri,omitempty"`
}

// AssetRecord is one tokenization record (collection: asset_records).
type AssetRecord struct {
	RecordID       string       `json:"recordId" bson:"_id"`
	ProjectID      string       `json:"projectId" bson:"projectId"`
	Status         RecordStatus `json:"status" bson:"status"`
	AssetRaw       []byte       `json:"-" bson:"assetRaw"`
	MetadataRaw    []byte       `json:"-" bson:"metadataRaw"` // full canonical metadata envelope, needed to rebuild the .rwa package
	Amount         string       `json:"amount" bson:"amount"`
	Unit           string       `json:"unit" bson:"unit"`
	Proofs         []Proof      `json:"proofs" bson:"proofs"`
	MetadataDigest string       `json:"metadataDigest" bson:"metadataDigest"`
	CID            string       `json:"cid" bson:"cid"`
	RecordKey      string       `json:"recordKey" bson:"recordKey"`
	Nonce          string       `json:"nonce" bson:"nonce"`
	ValidUntil     int64        `json:"validUntil" bson:"validUntil"`
	MintTxHash     string       `json:"mintTxHash,omitempty" bson:"mintTxHash,omitempty"`
	// Version is the optimistic-concurrency counter that makes a
	// reissue serialize by record id at the STORAGE layer (Mongo
	// UpdateConditional), not just in memory: the transition is conditional on
	// the version the caller last read, and bumps it, so two concurrent
	// reissues can't both roll the nonce — the loser's conditional update
	// simply fails instead of clobbering.
	// NOT omitempty: a freshly created record must persist version 0
	// explicitly so the first UpdateConditional's {version:0} filter matches
	// (a missing field does not equal 0 in a Mongo equality match).
	Version   int       `json:"-" bson:"version"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
}

// AuditPackage records that a .rwa package was built for a record
// (collection: audit_packages).
// AuditPackage's Frozen field, and every field below it, exist because the
// `.rwa` evidence package is regenerated from state on demand rather than
// retained immutably: without freezing, it could drift from the bytes the
// auditor actually signed. A record's package bytes themselves are
// deliberately NOT stored here or in IPFS (a separate platform decision) —
// instead, BuildPackage
// deterministically reconstructs identical bytes from the FROZEN fields
// below every time (see auditpkg.BuildPackage's determinism: identical
// inputs always produce an identical ZIP, proven by
// TestBuildPackageIsDeterministic), which is sufficient to guarantee "the
// same package that was actually signed" without duplicating storage.
//
// While the source AssetRecord is still Pending, RecordService.BuildPackage
// freely re-derives and Upserts this record from whatever is currently
// live (the server's current auditor, the record's current nonce/
// validUntil — which ReissueRecord may change) — Frozen stays false, and
// every field may legitimately change from one build to the next. The
// INSTANT RelaySignedResult successfully verifies a signature against it,
// the exact fields that verification used are captured here ONE FINAL TIME
// and Frozen is set true; from then on BuildPackage MUST reconstruct only
// from these frozen fields (never re-reading the server's current auditor
// or any other live state) so a later re-download, or an auditor rotation
// that happens after signing, can never silently produce package bytes
// inconsistent with the historical signature.
type AuditPackage struct {
	RecordID      string `json:"recordId" bson:"_id"`
	PackageSHA256 string `json:"packageSha256" bson:"packageSha256"`
	Size          int64  `json:"size" bson:"size"`
	// RecordVersion binds this package to the exact AssetRecord.Version it was
	// built from: relay accepts a signature only for a package
	// whose RecordVersion still equals the record's current version, so a
	// package built before a reissue (which bumps the record version) is
	// rejected as stale rather than silently satisfying the "a package exists"
	// gate for content/nonce the auditor never reviewed.
	RecordVersion int `json:"recordVersion" bson:"recordVersion"`
	// CID is the content CID of the metadata the package attests to, binding
	// the package to the content identity — the same value as the
	// source AssetRecord.CID at build time.
	CID string `json:"cid" bson:"cid"`
	// Frozen marks this record's fields below as permanent — see the type
	// doc comment. Never true->false.
	Frozen bool `json:"frozen" bson:"frozen"`
	// Auditor/ProfileDigest/MetadataDigest/RecordKey/Amount/Nonce/
	// ValidUntil/Vault are exactly the eip712.MintAttestation fields
	// BuildPackage embeds in typed-data.json — captured so a Frozen
	// package can be reconstructed byte-for-byte without consulting
	// anything else. Hex-encoded ("0x...") where the source is a [32]byte
	// digest or an address, decimal string for uint256 amounts, matching
	// AssetRecord's own string-encoding convention.
	Auditor         string    `json:"auditor" bson:"auditor"`
	ProfileDigest   string    `json:"profileDigest" bson:"profileDigest"`
	MetadataDigest  string    `json:"metadataDigest" bson:"metadataDigest"`
	RecordKey       string    `json:"recordKey" bson:"recordKey"`
	Amount          string    `json:"amount" bson:"amount"`
	Nonce           string    `json:"nonce" bson:"nonce"`
	ValidUntil      int64     `json:"validUntil" bson:"validUntil"`
	Vault           string    `json:"vault" bson:"vault"`
	TypedDataDigest string    `json:"typedDataDigest" bson:"typedDataDigest"`
	CreatedAt       time.Time `json:"createdAt" bson:"createdAt"`
}

// Attestation records a verified signed-result (collection: attestations).
type Attestation struct {
	RecordID        string    `json:"recordId" bson:"_id"`
	FormatVersion   string    `json:"formatVersion" bson:"formatVersion"`
	Auditor         string    `json:"auditor" bson:"auditor"`
	PrimaryType     string    `json:"primaryType" bson:"primaryType"`
	TypedDataDigest string    `json:"typedDataDigest" bson:"typedDataDigest"`
	Signature       string    `json:"signature" bson:"signature"`
	SignedAt        time.Time `json:"signedAt" bson:"signedAt"`
	Verified        bool      `json:"verified" bson:"verified"`
	TxHash          string    `json:"txHash,omitempty" bson:"txHash,omitempty"`
	CreatedAt       time.Time `json:"createdAt" bson:"createdAt"`
}
