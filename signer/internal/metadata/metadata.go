// Package metadata validates shared/schemas/metadata.schema.json rules, exposing
// issuance.amount/recordId for attestation binding and proof hashes for review
package metadata

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"regexp"
	"time"

	"github.com/rwa-platform/signer/internal/canonical"
	"github.com/rwa-platform/signer/internal/textsafe"
)

var (
	recordIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	amountPattern   = regexp.MustCompile(`^[0-9]+$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// maxIssuanceAmount is mint's uint256 maximum, matching
// server/internal/assets/record.go
var maxIssuanceAmount = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// Proof binds an external document to the record by content hash
type Proof struct {
	Type   string
	SHA256 string
	URI    string // optional; empty means not present
}

// Metadata is a validated, parsed envelope
type Metadata struct {
	PlatformVersion string
	ProjectID       string
	RecordID        string
	Asset           canonical.Value // arbitrary; validated separately against the profile's assetSchema
	IssuanceAmount  string          // decimal string, smallest-unit token amount
	IssuanceUnit    string
	Proofs          []Proof
	CreatedAt       string

	CanonicalBytes []byte
	Digest         [32]byte
}

// Validate checks metadata envelope rules (shared/schemas/metadata.schema.json) and
// canonical.DefaultLimits: recordId pattern, positive uint256 issuance.amount and
// lowercase proof hashes
func Validate(raw []byte) (*Metadata, error) {
	v, err := canonical.ParseWithLimits(raw, canonical.DefaultLimits())
	if err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	obj, ok := v.(*canonical.Object)
	if !ok {
		return nil, fmt.Errorf("metadata: document must be a JSON object")
	}

	required := []string{"platformVersion", "projectId", "recordId", "asset", "issuance", "createdAt"}
	for _, k := range required {
		if _, present := obj.Get(k); !present {
			return nil, fmt.Errorf("metadata: missing required field %q", k)
		}
	}
	allowed := map[string]bool{
		"platformVersion": true, "projectId": true, "recordId": true, "asset": true,
		"issuance": true, "proofs": true, "createdAt": true,
	}
	for _, k := range obj.Keys() {
		if !allowed[k] {
			return nil, fmt.Errorf("metadata: additional property %q is not allowed", k)
		}
	}

	m := &Metadata{}

	pv, _ := obj.Get("platformVersion")
	s, ok := pv.(string)
	if !ok || s != "1.0" {
		return nil, fmt.Errorf("metadata: platformVersion must be the string \"1.0\"")
	}
	m.PlatformVersion = s

	idv, _ := obj.Get("projectId")
	id, ok := idv.(string)
	if !ok || !uuidPattern.MatchString(id) {
		return nil, fmt.Errorf("metadata: projectId must be a UUID string")
	}
	m.ProjectID = id

	ridv, _ := obj.Get("recordId")
	rid, ok := ridv.(string)
	if !ok || !recordIDPattern.MatchString(rid) {
		return nil, fmt.Errorf("metadata: recordId must match ^[A-Za-z0-9._:-]{1,128}$")
	}
	m.RecordID = rid

	assetV, _ := obj.Get("asset")
	if _, ok := assetV.(*canonical.Object); !ok {
		return nil, fmt.Errorf("metadata: asset must be a JSON object")
	}
	m.Asset = assetV

	issV, _ := obj.Get("issuance")
	issObj, ok := issV.(*canonical.Object)
	if !ok {
		return nil, fmt.Errorf("metadata: issuance must be a JSON object")
	}
	for _, k := range issObj.Keys() {
		if k != "amount" && k != "unit" {
			return nil, fmt.Errorf("metadata: issuance: additional property %q is not allowed", k)
		}
	}
	amtV, present := issObj.Get("amount")
	if !present {
		return nil, fmt.Errorf("metadata: issuance.amount is required")
	}
	amt, ok := amtV.(string)
	if !ok || !amountPattern.MatchString(amt) {
		return nil, fmt.Errorf("metadata: issuance.amount must be a decimal string matching ^[0-9]+$")
	}
	// The digit pattern also accepts zero and oversized values; enforce positive uint256
	// as server/internal/assets/record.go does and SupplyController's ZeroAmount()
	// requires
	amtInt, ok := new(big.Int).SetString(amt, 10)
	if !ok || amtInt.Sign() <= 0 || amtInt.Cmp(maxIssuanceAmount) > 0 {
		return nil, fmt.Errorf("metadata: issuance.amount must be a positive integer no greater than 2^256-1 (the contract rejects a zero mint amount)")
	}
	m.IssuanceAmount = amt

	unitV, present := issObj.Get("unit")
	if !present {
		return nil, fmt.Errorf("metadata: issuance.unit is required")
	}
	unit, ok := unitV.(string)
	if !ok || unit == "" {
		return nil, fmt.Errorf("metadata: issuance.unit must be a non-empty string")
	}
	// Reject non-printable issuance.unit/proofs[].type before review; rendering also
	// escapes controls that could repaint amounts or digests
	if !textsafe.IsPrintable(unit) {
		return nil, fmt.Errorf("metadata: issuance.unit contains a control or non-printable character (%s); only printable text is allowed", textsafe.SanitizeForTerminal(unit))
	}
	m.IssuanceUnit = unit

	if proofsV, present := obj.Get("proofs"); present {
		arr, ok := proofsV.([]canonical.Value)
		if !ok {
			return nil, fmt.Errorf("metadata: proofs must be an array")
		}
		for i, e := range arr {
			pobj, ok := e.(*canonical.Object)
			if !ok {
				return nil, fmt.Errorf("metadata: proofs[%d] must be an object", i)
			}
			for _, k := range pobj.Keys() {
				if k != "type" && k != "sha256" && k != "uri" {
					return nil, fmt.Errorf("metadata: proofs[%d]: additional property %q is not allowed", i, k)
				}
			}
			typeV, ok1 := pobj.Get("type")
			shaV, ok2 := pobj.Get("sha256")
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("metadata: proofs[%d]: type and sha256 are required", i)
			}
			typ, ok := typeV.(string)
			if !ok || typ == "" {
				return nil, fmt.Errorf("metadata: proofs[%d].type must be a non-empty string", i)
			}
			if !textsafe.IsPrintable(typ) {
				return nil, fmt.Errorf("metadata: proofs[%d].type contains a control or non-printable character (%s); only printable text is allowed", i, textsafe.SanitizeForTerminal(typ))
			}
			sha, ok := shaV.(string)
			if !ok || !sha256Pattern.MatchString(sha) {
				return nil, fmt.Errorf("metadata: proofs[%d].sha256 must match ^[0-9a-f]{64}$", i)
			}
			proof := Proof{Type: typ, SHA256: sha}
			if uriV, present := pobj.Get("uri"); present {
				uri, ok := uriV.(string)
				if !ok {
					return nil, fmt.Errorf("metadata: proofs[%d].uri must be a string", i)
				}
				proof.URI = uri
			}
			m.Proofs = append(m.Proofs, proof)
		}
	}

	catV, _ := obj.Get("createdAt")
	cat, ok := catV.(string)
	if !ok {
		return nil, fmt.Errorf("metadata: createdAt must be a string")
	}
	if _, err := time.Parse(time.RFC3339, cat); err != nil {
		return nil, fmt.Errorf("metadata: createdAt must be an RFC 3339 date-time: %w", err)
	}
	m.CreatedAt = cat

	canonBytes, err := canonical.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("metadata: canonicalizing: %w", err)
	}
	m.CanonicalBytes = canonBytes
	m.Digest = sha256.Sum256(canonBytes)

	return m, nil
}
