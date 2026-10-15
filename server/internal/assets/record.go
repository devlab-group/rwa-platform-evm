package assets

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"

	"github.com/rwa-platform/server/internal/auditpkg"
)

var recordIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// decimalAmountPattern matches metadata.schema.json and the signer. It accepts zero and
// values above uint256, so ValidateRecord also enforces [1, maxIssuanceAmount];
// on-chain zero minting fails with ZeroAmount.
var decimalAmountPattern = regexp.MustCompile(`^[0-9]+$`)

// maxIssuanceAmount is 2^256-1, the largest uint256, since issuance.amount
// becomes the mint amount
var maxIssuanceAmount = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

var metadataAllowedTopLevelKeys = map[string]bool{
	"platformVersion": true, "projectId": true, "recordId": true, "asset": true,
	"issuance": true, "proofs": true, "createdAt": true,
}

// ProofRecord mirrors one shared/schemas/metadata.schema.json proofs[] entry
type ProofRecord struct {
	Type   string `json:"type"`
	SHA256 string `json:"sha256"`
	URI    string `json:"uri,omitempty"`
}

// Metadata is the parsed, validated metadata envelope
type Metadata struct {
	PlatformVersion string          `json:"platformVersion"`
	ProjectID       string          `json:"projectId"`
	RecordID        string          `json:"recordId"`
	Asset           json.RawMessage `json:"asset"`
	IssuanceAmount  string          `json:"issuanceAmount"`
	IssuanceUnit    string          `json:"issuanceUnit"`
	Proofs          []ProofRecord   `json:"proofs,omitempty"`
	CreatedAt       string          `json:"createdAt"`

	Canonical []byte
	Digest    [32]byte
	CID       string
}

// ValidateRecord checks the metadata envelope against metadata.schema.json and, with a
// non-nil profile, asset against assetSchema. Non-empty expectedAmount and
// expectedProjectID must match issuance.amount and projectId. Invalid documents return
// nil *Metadata and ValidationResult.Errors.
func ValidateRecord(profile *Profile, raw []byte, expectedAmount, expectedProjectID string) (ValidationResult, *Metadata) {
	var errs []string

	// as in ValidateProfile, the limits must hold before the asset object is
	// decoded into a generic tree for schema validation
	if err := auditpkg.CheckLimits(raw); err != nil {
		return ValidationResult{Valid: false, Errors: []string{err.Error()}}, nil
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return ValidationResult{Valid: false, Errors: []string{"record is not a JSON object: " + err.Error()}}, nil
	}
	for k := range top {
		if !metadataAllowedTopLevelKeys[k] {
			errs = append(errs, fmt.Sprintf("unknown top-level field %q (additionalProperties: false)", k))
		}
	}
	for _, req := range []string{"platformVersion", "projectId", "recordId", "asset", "issuance", "createdAt"} {
		if _, ok := top[req]; !ok {
			errs = append(errs, fmt.Sprintf("missing required field %q", req))
		}
	}
	if len(errs) > 0 {
		return ValidationResult{Valid: false, Errors: errs}, nil
	}

	m := &Metadata{}
	var platformVersion string
	if err := json.Unmarshal(top["platformVersion"], &platformVersion); err != nil || platformVersion != "1.0" {
		errs = append(errs, `platformVersion must be the string "1.0"`)
	}
	m.PlatformVersion = platformVersion

	if err := json.Unmarshal(top["projectId"], &m.ProjectID); err != nil || !uuidPattern.MatchString(m.ProjectID) {
		errs = append(errs, "projectId must be a UUID string")
	} else if expectedProjectID != "" && m.ProjectID != expectedProjectID {
		errs = append(errs, "projectId does not match the immutable project profile")
	}

	if err := json.Unmarshal(top["recordId"], &m.RecordID); err != nil || !recordIDPattern.MatchString(m.RecordID) {
		errs = append(errs, `recordId must match ^[A-Za-z0-9._:-]{1,128}$`)
	}

	m.Asset = top["asset"]
	var assetObj map[string]json.RawMessage
	if err := json.Unmarshal(m.Asset, &assetObj); err != nil {
		errs = append(errs, "asset must be a JSON object")
	} else if profile != nil && profile.compiledAssetSchema != nil {
		var assetInstance any
		if err := json.Unmarshal(m.Asset, &assetInstance); err != nil {
			errs = append(errs, "asset is not valid JSON: "+err.Error())
		} else if err := profile.compiledAssetSchema.Validate(assetInstance); err != nil {
			errs = append(errs, "asset does not satisfy the project asset schema: "+err.Error())
		}
	}

	var issuance struct {
		Amount string `json:"amount"`
		Unit   string `json:"unit"`
	}
	// issuance sets additionalProperties to false in the schema, so decode strictly
	if err := unmarshalStrict(top["issuance"], &issuance); err != nil {
		errs = append(errs, "issuance must be an object with string amount/unit and no additional fields")
	} else {
		if !decimalAmountPattern.MatchString(issuance.Amount) {
			errs = append(errs, "issuance.amount must match ^[0-9]+$")
		} else if n, ok := new(big.Int).SetString(issuance.Amount, 10); !ok || n.Sign() <= 0 || n.Cmp(maxIssuanceAmount) > 0 {
			// the pattern matched but the value is zero or does not fit in a uint256
			errs = append(errs, "issuance.amount must be a positive integer no greater than 2^256-1 (the contract rejects a zero mint amount)")
		} else if expectedAmount != "" && issuance.Amount != expectedAmount {
			errs = append(errs, "issuance.amount must equal the record's amount / EIP-712 amount")
		}
		if issuance.Unit == "" {
			errs = append(errs, "issuance.unit must be non-empty")
		}
		m.IssuanceAmount = issuance.Amount
		m.IssuanceUnit = issuance.Unit
	}

	if proofsRaw, ok := top["proofs"]; ok {
		var proofs []ProofRecord
		// proofs[] items set additionalProperties to false in the schema
		if err := unmarshalStrict(proofsRaw, &proofs); err != nil {
			errs = append(errs, "proofs must be an array of {type, sha256, uri?} with no additional fields")
		} else {
			for i, pr := range proofs {
				if pr.Type == "" {
					errs = append(errs, fmt.Sprintf("proofs[%d].type must be non-empty", i))
				}
				if !sha256HexPattern.MatchString(pr.SHA256) {
					errs = append(errs, fmt.Sprintf("proofs[%d].sha256 must match ^[0-9a-f]{64}$", i))
				}
			}
			m.Proofs = proofs
		}
	}

	if err := json.Unmarshal(top["createdAt"], &m.CreatedAt); err != nil || !isRFC3339(m.CreatedAt) {
		errs = append(errs, "createdAt must be an RFC3339 date-time string")
	}

	if len(errs) > 0 {
		return ValidationResult{Valid: false, Errors: errs}, nil
	}

	canonical, digest, cidStr, err := auditpkg.CanonicalizeAndDigest(raw)
	if err != nil {
		return ValidationResult{Valid: false, Errors: []string{"canonicalization failed: " + err.Error()}}, nil
	}
	m.Canonical = canonical
	m.Digest = digest
	m.CID = cidStr

	return ValidationResult{
		Valid:         true,
		Errors:        []string{},
		ProfileDigest: "",
		CID:           cidStr,
	}, m
}

// DigestHex returns the metadata digest as a 0x-prefixed hex string
func (m *Metadata) DigestHex() string {
	return "0x" + hex.EncodeToString(m.Digest[:])
}

// DigestHex returns the profile digest as a 0x-prefixed hex string
func (p *Profile) DigestHex() string {
	return "0x" + hex.EncodeToString(p.Digest[:])
}
