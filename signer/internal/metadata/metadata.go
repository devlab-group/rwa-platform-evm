// Package metadata validates a metadata envelope document against the
// fixed platform schema (shared/schemas/metadata.schema.json, architecture
// §6.2) and exposes the parsed fields the signer must cross-check against
// the typed attestation (issuance.amount, recordId, proof hashes).
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

// maxIssuanceAmount is uint256's maximum value -- every issuance.amount
// eventually becomes a Solidity uint256 mint amount (SupplyController.mint).
// Mirrors server/internal/assets/record.go's identical constant.
var maxIssuanceAmount = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// Proof binds an external document to the record by content hash.
type Proof struct {
	Type   string
	SHA256 string
	URI    string // optional; empty means not present
}

// Metadata is a validated, parsed metadata envelope.
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

// Validate parses and validates raw metadata envelope JSON against
// shared/schemas/metadata.schema.json plus the platform rules (recordId
// pattern, issuance.amount is a decimal string, proofs[].sha256 is hex).
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
	// amountPattern alone accepts "0" and arbitrarily large numerals, but
	// SupplyController.mint reverts ZeroAmount() on a zero attestation amount
	// and the value must fit a uint256. Mirror the identical semantic bound
	// in server/internal/assets/record.go exactly so the two validators agree
	// on every amount string -- otherwise the signer would happily sign a
	// zero-amount attestation the server and contract both reject.
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
	// unit and proofs[].type are both rendered on the offline review screen.
	// A control character there is never legitimate and is how a package would
	// try to move the terminal cursor over the amount or digest the auditor is
	// confirming, so refuse the document rather than lean on the review
	// screen's own escaping alone.
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
