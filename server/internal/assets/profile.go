// Package assets validates Asset Profiles and metadata records against the
// FROZEN envelope schemas in shared/schemas/*.json (mirrored here in Go
// since the envelopes are small and fixed; the operator-defined
// `assetSchema` inside a profile is genuinely dynamic and is validated with
// a real JSON Schema compiler, see schema_guard.go). See package
// internal/auditpkg_test's cross-check against the actual shared/schemas
// files, and internal/assets' own equivalent below, for drift protection.
package assets

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/rwa-platform/server/internal/auditpkg"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// ValidationResult mirrors components.schemas.ValidationResult in api/openapi.yaml.
type ValidationResult struct {
	Valid         bool     `json:"valid"`
	Errors        []string `json:"errors"`
	ProfileDigest string   `json:"profileDigest,omitempty"`
	CID           string   `json:"cid,omitempty"`
}

// DisplayField is one profile.displayFields entry.
type DisplayField struct {
	Label   string `json:"label"`
	Pointer string `json:"pointer"`
}

// Profile is the parsed, validated Asset Profile.
type Profile struct {
	ProfileVersion string          `json:"profileVersion"`
	ProjectID      string          `json:"projectId"`
	AssetType      string          `json:"assetType"`
	TokenUnit      string          `json:"tokenUnit"`
	TokenDecimals  uint8           `json:"tokenDecimals"`
	RecordIDLabel  string          `json:"recordIdLabel"`
	DisplayFields  []DisplayField  `json:"displayFields,omitempty"`
	AssetSchema    json.RawMessage `json:"assetSchema"`

	Canonical []byte   `json:"-"`
	Digest    [32]byte `json:"-"`
	CID       string   `json:"-"`

	compiledAssetSchema *jsonschema.Schema
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var profileAllowedTopLevelKeys = map[string]bool{
	"profileVersion": true, "projectId": true, "assetType": true, "tokenUnit": true,
	"tokenDecimals": true, "recordIdLabel": true, "displayFields": true, "assetSchema": true,
}

// ValidateProfile parses and validates a raw Asset Profile document against
// shared/schemas/asset-profile.schema.json. It always returns a non-nil
// ValidationResult; a nil *Profile means the document was invalid (see
// Errors). Structural/semantic problems are collected as errors rather than a
// Go error, matching the always-200 validateProfile API.
func ValidateProfile(raw []byte) (ValidationResult, *Profile) {
	var errs []string

	// Enforce the document size/depth/string/array/object limits BEFORE
	// guardAssetSchema/compileAssetSchema get a chance to recurse into the
	// (still fully untrusted) assetSchema tree via a generic `any` unmarshal —
	// see auditpkg.CheckLimits' doc comment.
	if err := auditpkg.CheckLimits(raw); err != nil {
		return ValidationResult{Valid: false, Errors: []string{err.Error()}}, nil
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return ValidationResult{Valid: false, Errors: []string{"profile is not a JSON object: " + err.Error()}}, nil
	}
	for k := range top {
		if !profileAllowedTopLevelKeys[k] {
			errs = append(errs, fmt.Sprintf("unknown top-level field %q (additionalProperties: false)", k))
		}
	}
	for _, req := range []string{"profileVersion", "projectId", "assetType", "tokenUnit", "tokenDecimals", "recordIdLabel", "assetSchema"} {
		if _, ok := top[req]; !ok {
			errs = append(errs, fmt.Sprintf("missing required field %q", req))
		}
	}
	if len(errs) > 0 {
		return ValidationResult{Valid: false, Errors: errs}, nil
	}

	p := &Profile{}
	if err := json.Unmarshal(top["profileVersion"], &p.ProfileVersion); err != nil || p.ProfileVersion != "1.0" {
		errs = append(errs, `profileVersion must be the string "1.0"`)
	}
	if err := json.Unmarshal(top["projectId"], &p.ProjectID); err != nil || !uuidPattern.MatchString(p.ProjectID) {
		errs = append(errs, "projectId must be a UUID string")
	}
	if err := json.Unmarshal(top["assetType"], &p.AssetType); err != nil || len(p.AssetType) < 1 || len(p.AssetType) > 128 {
		errs = append(errs, "assetType must be a string of length 1..128")
	}
	if err := json.Unmarshal(top["tokenUnit"], &p.TokenUnit); err != nil || len(p.TokenUnit) < 1 || len(p.TokenUnit) > 64 {
		errs = append(errs, "tokenUnit must be a string of length 1..64")
	}
	var decimals float64
	if err := json.Unmarshal(top["tokenDecimals"], &decimals); err != nil || decimals != float64(int64(decimals)) || decimals < 0 || decimals > 36 {
		errs = append(errs, "tokenDecimals must be an integer in [0,36]")
	} else {
		p.TokenDecimals = uint8(decimals)
	}
	if err := json.Unmarshal(top["recordIdLabel"], &p.RecordIDLabel); err != nil || len(p.RecordIDLabel) < 1 || len(p.RecordIDLabel) > 128 {
		errs = append(errs, "recordIdLabel must be a string of length 1..128")
	}
	if raw, ok := top["displayFields"]; ok {
		var fields []DisplayField
		// Strict: shared/schemas/asset-profile.schema.json declares
		// displayFields[] items additionalProperties:false; the signer rejects
		// an unknown field here and the server must too, instead of silently
		// dropping it via a lenient json.Unmarshal.
		if err := unmarshalStrict(raw, &fields); err != nil {
			errs = append(errs, "displayFields must be an array of {label, pointer} with no additional fields")
		} else {
			for i, f := range fields {
				if f.Label == "" {
					errs = append(errs, fmt.Sprintf("displayFields[%d].label must be non-empty", i))
				}
				if len(f.Pointer) == 0 || f.Pointer[0] != '/' {
					errs = append(errs, fmt.Sprintf("displayFields[%d].pointer must start with '/'", i))
				}
			}
			p.DisplayFields = fields
		}
	}

	p.AssetSchema = top["assetSchema"]
	if err := guardAssetSchema(p.AssetSchema); err != nil {
		errs = append(errs, err.Error())
	} else if compiled, err := compileAssetSchema(p.AssetSchema); err != nil {
		errs = append(errs, "assetSchema does not compile: "+err.Error())
	} else {
		p.compiledAssetSchema = compiled
	}

	if len(errs) > 0 {
		return ValidationResult{Valid: false, Errors: errs}, nil
	}

	canonical, digest, cidStr, err := auditpkg.CanonicalizeAndDigest(raw)
	if err != nil {
		return ValidationResult{Valid: false, Errors: []string{"canonicalization failed: " + err.Error()}}, nil
	}
	p.Canonical = canonical
	p.Digest = digest
	p.CID = cidStr

	return ValidationResult{
		Valid:         true,
		Errors:        []string{},
		ProfileDigest: "0x" + hex.EncodeToString(digest[:]),
		CID:           cidStr,
	}, p
}

func compileAssetSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020
	// LoadURL is overridden (rather than left at the library default) so a
	// $ref requiring network I/O fails closed with an explicit error instead
	// of depending on the http loader never having been imported elsewhere
	// in the binary. Local $ref (starting with '#') never calls LoadURL.
	c.LoadURL = func(s string) (io.ReadCloser, error) {
		return nil, errors.New("remote schema references are forbidden: " + s)
	}
	if err := c.AddResource("assetSchema.json", bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return c.Compile("assetSchema.json")
}

// isRFC3339 validates the metadata.createdAt "date-time" format.
func isRFC3339(s string) bool {
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}
