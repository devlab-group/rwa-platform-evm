package models

import "time"

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
