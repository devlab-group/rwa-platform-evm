// Package profile validates an Asset Profile document against the fixed
// platform envelope (shared/schemas/asset-profile.schema.json) and compiles
// its operator-defined assetSchema for later use validating individual asset
// records.
package profile

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/rwa-platform/signer/internal/canonical"
	"github.com/rwa-platform/signer/internal/jsonschema"
	"github.com/rwa-platform/signer/internal/textsafe"
)

// DisplayField is one entry of the offline review screen field list.
// displayFields controls that screen only; it never affects hashing or
// validation.
type DisplayField struct {
	Label   string
	Pointer string
}

// Profile is a validated, parsed Asset Profile.
type Profile struct {
	ProfileVersion string
	ProjectID      string
	AssetType      string
	TokenUnit      string
	TokenDecimals  int
	RecordIDLabel  string
	DisplayFields  []DisplayField
	AssetSchema    *jsonschema.Schema

	// Canonical bytes and digest of the whole profile document, as stored
	// on-chain and referenced by MintAttestation.profileDigest.
	CanonicalBytes []byte
	Digest         [32]byte
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Validate parses and validates raw Asset Profile JSON against the fixed
// platform envelope (shared/schemas/asset-profile.schema.json), then
// compiles assetSchema via the restricted jsonschema subset engine.
func Validate(raw []byte) (*Profile, error) {
	v, err := canonical.ParseWithLimits(raw, canonical.DefaultLimits())
	if err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	obj, ok := v.(*canonical.Object)
	if !ok {
		return nil, fmt.Errorf("profile: document must be a JSON object")
	}

	required := []string{"profileVersion", "projectId", "assetType", "tokenUnit", "tokenDecimals", "recordIdLabel", "assetSchema"}
	for _, k := range required {
		if _, present := obj.Get(k); !present {
			return nil, fmt.Errorf("profile: missing required field %q", k)
		}
	}
	allowed := map[string]bool{
		"profileVersion": true, "projectId": true, "assetType": true, "tokenUnit": true,
		"tokenDecimals": true, "recordIdLabel": true, "displayFields": true, "assetSchema": true,
	}
	for _, k := range obj.Keys() {
		if !allowed[k] {
			return nil, fmt.Errorf("profile: additional property %q is not allowed", k)
		}
	}

	p := &Profile{}

	pv, _ := obj.Get("profileVersion")
	s, ok := pv.(string)
	if !ok || s != "1.0" {
		return nil, fmt.Errorf("profile: profileVersion must be the string \"1.0\"")
	}
	p.ProfileVersion = s

	idv, _ := obj.Get("projectId")
	id, ok := idv.(string)
	if !ok || !uuidPattern.MatchString(id) {
		return nil, fmt.Errorf("profile: projectId must be a UUID string")
	}
	p.ProjectID = id

	assetType, err := requireBoundedString(obj, "assetType", 1, 128)
	if err != nil {
		return nil, err
	}
	p.AssetType = assetType

	tokenUnit, err := requireBoundedString(obj, "tokenUnit", 1, 64)
	if err != nil {
		return nil, err
	}
	p.TokenUnit = tokenUnit

	tdv, _ := obj.Get("tokenDecimals")
	tdNum, ok := tdv.(json.Number)
	if !ok {
		return nil, fmt.Errorf("profile: tokenDecimals must be an integer")
	}
	td, err := tdNum.Int64()
	if err != nil || td < 0 || td > 36 {
		return nil, fmt.Errorf("profile: tokenDecimals must be an integer in [0,36]")
	}
	p.TokenDecimals = int(td)

	recordIDLabel, err := requireBoundedString(obj, "recordIdLabel", 1, 128)
	if err != nil {
		return nil, err
	}
	p.RecordIDLabel = recordIDLabel

	if dfv, present := obj.Get("displayFields"); present {
		arr, ok := dfv.([]canonical.Value)
		if !ok {
			return nil, fmt.Errorf("profile: displayFields must be an array")
		}
		for i, e := range arr {
			dobj, ok := e.(*canonical.Object)
			if !ok {
				return nil, fmt.Errorf("profile: displayFields[%d] must be an object", i)
			}
			for _, k := range dobj.Keys() {
				if k != "label" && k != "pointer" {
					return nil, fmt.Errorf("profile: displayFields[%d]: additional property %q is not allowed", i, k)
				}
			}
			labelV, ok1 := dobj.Get("label")
			pointerV, ok2 := dobj.Get("pointer")
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("profile: displayFields[%d]: label and pointer are required", i)
			}
			label, ok := labelV.(string)
			if !ok || label == "" {
				return nil, fmt.Errorf("profile: displayFields[%d].label must be a non-empty string", i)
			}
			if !textsafe.IsPrintable(label) {
				return nil, fmt.Errorf("profile: displayFields[%d].label contains a control or non-printable character (%s); only printable text is allowed", i, textsafe.SanitizeForTerminal(label))
			}
			pointer, ok := pointerV.(string)
			if !ok || len(pointer) == 0 || pointer[0] != '/' {
				return nil, fmt.Errorf("profile: displayFields[%d].pointer must start with \"/\"", i)
			}
			p.DisplayFields = append(p.DisplayFields, DisplayField{Label: label, Pointer: pointer})
		}
	}

	asv, _ := obj.Get("assetSchema")
	asObj, ok := asv.(*canonical.Object)
	if !ok {
		return nil, fmt.Errorf("profile: assetSchema must be a JSON object")
	}
	schema, err := jsonschema.Compile(asObj)
	if err != nil {
		return nil, fmt.Errorf("profile: assetSchema: %w", err)
	}
	p.AssetSchema = schema

	canonBytes, err := canonical.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("profile: canonicalizing: %w", err)
	}
	p.CanonicalBytes = canonBytes
	p.Digest = sha256.Sum256(canonBytes)

	return p, nil
}

// ValidateAsset validates the operator-defined "asset" object of a metadata
// record against this profile's assetSchema.
func (p *Profile) ValidateAsset(assetJSON canonical.Value) error {
	if p.AssetSchema == nil {
		return fmt.Errorf("profile: assetSchema is not compiled")
	}
	return p.AssetSchema.Validate(assetJSON)
}

// requireBoundedString reads a string field and enforces both a rune-length
// bound and a printable charset. The charset check matters most for tokenUnit:
// it is rendered on the review screen on the same line as the normalized
// amount, and unlike chainId or vault it is not pinned by the --policy trust
// root, so nothing else constrains what a package can put there. A control
// character in it has no legitimate meaning, so reject the document outright
// rather than rely on the review screen escaping it.
func requireBoundedString(obj *canonical.Object, key string, min, max int) (string, error) {
	v, _ := obj.Get(key)
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("profile: %s must be a string", key)
	}
	n := len([]rune(s))
	if n < min || n > max {
		return "", fmt.Errorf("profile: %s length %d is out of bounds [%d,%d]", key, n, min, max)
	}
	if !textsafe.IsPrintable(s) {
		return "", fmt.Errorf("profile: %s contains a control or non-printable character (%s); only printable text is allowed", key, textsafe.SanitizeForTerminal(s))
	}
	return s, nil
}
