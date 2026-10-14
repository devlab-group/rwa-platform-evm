// Package profile validates an Asset Profile document against the platform envelope
// (shared/schemas/asset-profile.schema.json) and compiles its operator-defined
// assetSchema for validating asset records
package profile

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/rwa-platform/signer/internal/canonical"
	"github.com/rwa-platform/signer/internal/jsonschema"
	"github.com/rwa-platform/signer/internal/textsafe"
)

// DisplayField is one entry of the review screen's field list. The definitions are part
// of the profile document and so covered by profileDigest, but resolving them only
// affects what the screen shows, never validation
type DisplayField struct {
	Label   string
	Pointer string
}

// DocRef pairs a legal document URI with its pinned hash for display; the signer never fetches it
type DocRef struct {
	URI    string
	SHA256 string
}

// Rating is the legal descriptor's rating reference
type Rating struct {
	Provider string
	Grade    string
	Ref      DocRef
}

// Legal is the validated optional legal descriptor; empty strings and nil pointers mark absent members
type Legal struct {
	Wrapper         string
	IssuerLEI       string
	HolderRights    string
	RedemptionTerms *DocRef
	MaturityDate    string
	Rating          *Rating
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
	Legal          *Legal
	AssetSchema    *jsonschema.Schema

	// Canonical bytes and SHA-256 digest of the whole profile document; the digest is
	// MintAttestation.profileDigest
	CanonicalBytes []byte
	Digest         [32]byte
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Validate parses and validates raw Asset Profile JSON against the platform envelope
// (shared/schemas/asset-profile.schema.json) and the structural limits in
// canonical.DefaultLimits, then compiles assetSchema with the jsonschema package
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
		"tokenDecimals": true, "recordIdLabel": true, "displayFields": true, "legal": true, "assetSchema": true,
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

	if lv, present := obj.Get("legal"); present {
		p.Legal, err = parseLegal(lv)
		if err != nil {
			return nil, err
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

// ValidateAsset validates the operator-defined "asset" object of a metadata record
// against this profile's assetSchema
func (p *Profile) ValidateAsset(assetJSON canonical.Value) error {
	if p.AssetSchema == nil {
		return fmt.Errorf("profile: assetSchema is not compiled")
	}
	return p.AssetSchema.Validate(assetJSON)
}

// requireBoundedString enforces rune-length bounds and printable characters.
// Reject controls before review, especially in tokenUnit, which appears beside
// the normalized amount and is not pinned by policy. Rendering still escapes
// free-form values.
func requireBoundedString(obj *canonical.Object, key string, min, max int) (string, error) {
	return boundedString(obj, key, key, min, max)
}

// boundedString checks a string at key and reports errors under name, such as legal.rating.uri
func boundedString(obj *canonical.Object, name, key string, min, max int) (string, error) {
	v, _ := obj.Get(key)
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("profile: %s must be a string", name)
	}
	n := len([]rune(s))
	if n < min || n > max {
		return "", fmt.Errorf("profile: %s length %d is out of bounds [%d,%d]", name, n, min, max)
	}
	if !textsafe.IsPrintable(s) {
		return "", fmt.Errorf("profile: %s contains a control or non-printable character (%s); only printable text is allowed", name, textsafe.SanitizeForTerminal(s))
	}
	return s, nil
}

var (
	leiPattern    = regexp.MustCompile(`^[A-Z0-9]{18}[0-9]{2}$`)
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// validLEI checks the LEI pattern and ISO 7064 MOD 97-10: expand A-Z to 10-35, then require remainder 1 mod 97
func validLEI(s string) bool {
	if !leiPattern.MatchString(s) {
		return false
	}
	var sb strings.Builder
	for _, c := range s {
		if c >= 'A' {
			fmt.Fprintf(&sb, "%d", c-'A'+10)
		} else {
			sb.WriteRune(c)
		}
	}
	n, _ := new(big.Int).SetString(sb.String(), 10)
	return n.Mod(n, big.NewInt(97)).Int64() == 1
}

// legalObject requires an object containing only allowed members
func legalObject(v canonical.Value, name string, allowed ...string) (*canonical.Object, error) {
	o, ok := v.(*canonical.Object)
	if !ok {
		return nil, fmt.Errorf("profile: %s must be an object", name)
	}
	for _, k := range o.Keys() {
		known := false
		for _, a := range allowed {
			known = known || a == k
		}
		if !known {
			return nil, fmt.Errorf("profile: %s: additional property %q is not allowed", name, k)
		}
	}
	return o, nil
}

// legalString reads bounded printable text; an absent optional member yields ""
func legalString(o *canonical.Object, name, key string, min, max int, required bool) (string, error) {
	if _, present := o.Get(key); !present {
		if required {
			return "", fmt.Errorf("profile: %s.%s is required", name, key)
		}
		return "", nil
	}
	return boundedString(o, name+"."+key, key, min, max)
}

func parseDocRef(o *canonical.Object, name string) (DocRef, error) {
	uri, err := legalString(o, name, "uri", 1, 2048, true)
	if err != nil {
		return DocRef{}, err
	}
	if strings.IndexFunc(uri, unicode.IsSpace) >= 0 {
		return DocRef{}, fmt.Errorf("profile: %s.uri must not contain whitespace", name)
	}
	sum, err := legalString(o, name, "sha256", 64, 64, true)
	if err != nil {
		return DocRef{}, err
	}
	if !sha256Pattern.MatchString(sum) {
		return DocRef{}, fmt.Errorf("profile: %s.sha256 must be 64 lowercase hex characters", name)
	}
	return DocRef{URI: uri, SHA256: sum}, nil
}

func parseLegal(v canonical.Value) (*Legal, error) {
	o, err := legalObject(v, "legal", "wrapper", "issuerLei", "holderRights", "redemptionTerms", "maturityDate", "rating")
	if err != nil {
		return nil, err
	}
	l := &Legal{}
	if l.Wrapper, err = legalString(o, "legal", "wrapper", 1, 32, true); err != nil {
		return nil, err
	}
	switch l.Wrapper {
	case "share", "entitlement", "certificate", "claim":
	default:
		return nil, fmt.Errorf("profile: legal.wrapper must be one of share, entitlement, certificate, claim")
	}
	if l.IssuerLEI, err = legalString(o, "legal", "issuerLei", 1, 64, false); err != nil {
		return nil, err
	}
	if _, present := o.Get("issuerLei"); present && !validLEI(l.IssuerLEI) {
		return nil, fmt.Errorf("profile: legal.issuerLei is not a valid LEI (pattern or ISO 7064 MOD 97-10 check digits)")
	}
	if l.HolderRights, err = legalString(o, "legal", "holderRights", 1, 2048, false); err != nil {
		return nil, err
	}
	if rv, present := o.Get("redemptionTerms"); present {
		ro, err := legalObject(rv, "legal.redemptionTerms", "uri", "sha256")
		if err != nil {
			return nil, err
		}
		ref, err := parseDocRef(ro, "legal.redemptionTerms")
		if err != nil {
			return nil, err
		}
		l.RedemptionTerms = &ref
	}
	if l.MaturityDate, err = legalString(o, "legal", "maturityDate", 1, 64, false); err != nil {
		return nil, err
	}
	if _, present := o.Get("maturityDate"); present {
		if t, perr := time.Parse("2006-01-02", l.MaturityDate); perr != nil || t.Format("2006-01-02") != l.MaturityDate {
			return nil, fmt.Errorf("profile: legal.maturityDate must be a calendar date in YYYY-MM-DD form")
		}
	}
	if rv, present := o.Get("rating"); present {
		ro, err := legalObject(rv, "legal.rating", "provider", "grade", "uri", "sha256")
		if err != nil {
			return nil, err
		}
		r := &Rating{}
		if r.Provider, err = legalString(ro, "legal.rating", "provider", 1, 128, true); err != nil {
			return nil, err
		}
		if r.Grade, err = legalString(ro, "legal.rating", "grade", 1, 32, false); err != nil {
			return nil, err
		}
		if r.Ref, err = parseDocRef(ro, "legal.rating"); err != nil {
			return nil, err
		}
		l.Rating = r
	}
	return l, nil
}
