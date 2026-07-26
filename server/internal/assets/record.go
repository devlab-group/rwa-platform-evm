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

// decimalAmountPattern is shared/schemas/metadata.schema.json's exact
// issuance.amount pattern (frozen envelope syntax — must not diverge from
// it or from the signer's identical check). It alone is not sufficient:
// "0" satisfies it but SupplyController.mint always `revert ZeroAmount()`s
// on a zero attestation amount — the regex accepts zero, so without a further
// check the server could persist a record the contract will always reject.
// maxIssuanceAmount below adds the semantic (0, 2^256-1] bound the pattern
// can't express, without changing the pattern itself.
var decimalAmountPattern = regexp.MustCompile(`^[0-9]+$`)

// maxIssuanceAmount is uint256's maximum value — every issuance.amount
// eventually becomes a Solidity uint256 mint amount.
var maxIssuanceAmount = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

var metadataAllowedTopLevelKeys = map[string]bool{
	"platformVersion": true, "projectId": true, "recordId": true, "asset": true,
	"issuance": true, "proofs": true, "createdAt": true,
}

// ProofRecord mirrors one shared/schemas/metadata.schema.json proofs[] entry.
type ProofRecord struct {
	Type   string `json:"type"`
	SHA256 string `json:"sha256"`
	URI    string `json:"uri,omitempty"`
}

// Metadata is the parsed, validated metadata envelope.
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

// ValidateRecord parses and validates a raw metadata envelope document
// against shared/schemas/metadata.schema.json and, for the nested `asset`
// object, against the project's Asset Profile assetSchema. expectedAmount,
// if non-empty, must equal issuance.amount (which in turn must equal the
// EIP-712 amount); expectedProjectID, if non-empty, must equal
// metadata.projectId (which must equal the immutable project profile).
func ValidateRecord(profile *Profile, raw []byte, expectedAmount, expectedProjectID string) (ValidationResult, *Metadata) {
	var errs []string

	// Same reasoning as ValidateProfile — bound size/depth/string/array/object
	// BEFORE the nested `asset` object is unmarshaled into a generic `any`
	// tree for JSON-Schema validation below.
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
	// Strict: shared/schemas/metadata.schema.json declares issuance
	// additionalProperties:false; see unmarshalStrict's doc comment.
	if err := unmarshalStrict(top["issuance"], &issuance); err != nil {
		errs = append(errs, "issuance must be an object with string amount/unit and no additional fields")
	} else {
		if !decimalAmountPattern.MatchString(issuance.Amount) {
			errs = append(errs, "issuance.amount must match ^[0-9]+$")
		} else if n, ok := new(big.Int).SetString(issuance.Amount, 10); !ok || n.Sign() <= 0 || n.Cmp(maxIssuanceAmount) > 0 {
			// Reachable even though the regex above matched — e.g. "0", or a
			// numeral with more digits than fit in a uint256.
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
		// Strict: shared/schemas/metadata.schema.json declares proofs[] items
		// additionalProperties:false.
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

// DigestHex returns the metadata digest as a 0x-prefixed hex string.
func (m *Metadata) DigestHex() string {
	return "0x" + hex.EncodeToString(m.Digest[:])
}

// DigestHex returns the profile digest as a 0x-prefixed hex string.
func (p *Profile) DigestHex() string {
	return "0x" + hex.EncodeToString(p.Digest[:])
}
