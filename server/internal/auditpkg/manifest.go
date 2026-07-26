package auditpkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Manifest mirrors shared/schemas/rwa-manifest.schema.json.
type Manifest struct {
	PackageVersion string         `json:"packageVersion"`
	PrimaryType    string         `json:"primaryType"` // "MintAttestation" | "BurnAttestation"
	ProfileDigest  string         `json:"profileDigest"`
	MetadataDigest string         `json:"metadataDigest"`
	Files          []ManifestFile `json:"files"`
}

// ManifestFile is one entry of Manifest.Files.
type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	MIME   string `json:"mime"`
}

// TypedDataDoc is the package's typed-data.json, matching
// shared/schemas/typed-data.schema.json byte-for-byte (additionalProperties:
// false — the server and the offline signer MUST agree on these field names
// exactly). There is deliberately no digest field here: the signer
// recomputes the EIP-712 digest itself from domain+message rather than
// trusting a server-supplied value.
type TypedDataDoc struct {
	PrimaryType string           `json:"primaryType"`
	Domain      TypedDataDomain  `json:"domain"`
	Message     TypedDataMessage `json:"message"`
}

// TypedDataDomain mirrors the EIP712Domain used for every attestation.
type TypedDataDomain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           int64  `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

// TypedDataMessage mirrors typed-data.schema.json's message object. amount
// and nonce are decimal strings, not JSON numbers, per the schema pattern.
// recordId/recordKey are set (and operationId omitted) for MintAttestation;
// operationId is set (and recordId/recordKey omitted) for BurnAttestation.
type TypedDataMessage struct {
	Auditor        string `json:"auditor"`
	ProfileDigest  string `json:"profileDigest"`
	RecordID       string `json:"recordId,omitempty"`
	RecordKey      string `json:"recordKey,omitempty"`
	OperationID    string `json:"operationId,omitempty"`
	MetadataDigest string `json:"metadataDigest"`
	Amount         string `json:"amount"`
	Nonce          string `json:"nonce"`
	ValidUntil     int64  `json:"validUntil"`
	Vault          string `json:"vault"`
}

// ProofFile is one offline-only or public attachment bundled under proofs/.
type ProofFile struct {
	Name string
	MIME string
	Data []byte
}

// mandatoryManifestFiles are the only manifest.files entries permitted
// outside proofs/ — every other declared file MUST live under proofs/.
var mandatoryManifestFiles = map[string]bool{
	"profile.json":    true,
	"metadata.json":   true,
	"typed-data.json": true,
}

var (
	manifestDigestPattern = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	manifestPathPattern   = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)
	manifestSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// maxManifestFileSize mirrors shared/schemas/rwa-manifest.schema.json's
// files[].size maximum (100 MiB).
const maxManifestFileSize = 104857600

// parseManifestStrict decodes and validates raw as manifest.json against
// shared/schemas/rwa-manifest.schema.json's exact constraints — a strict
// decode rather than a permissive json.Unmarshal, so an unknown or malformed
// field is rejected. It additionally enforces the file-set rules the JSON
// Schema alone can't express: no duplicate declared paths, the three mandatory
// JSON files present exactly once, and every other entry confined to proofs/.
func parseManifestStrict(raw []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("auditpkg: manifest.json failed strict decoding: %w", err)
	}
	if dec.More() {
		return Manifest{}, fmt.Errorf("auditpkg: manifest.json has trailing content after the JSON value")
	}

	if m.PackageVersion != "1.0" {
		return Manifest{}, fmt.Errorf("auditpkg: manifest packageVersion must be \"1.0\", got %q", m.PackageVersion)
	}
	if m.PrimaryType != "MintAttestation" && m.PrimaryType != "BurnAttestation" {
		return Manifest{}, fmt.Errorf("auditpkg: manifest primaryType must be MintAttestation or BurnAttestation, got %q", m.PrimaryType)
	}
	if !manifestDigestPattern.MatchString(m.ProfileDigest) {
		return Manifest{}, fmt.Errorf("auditpkg: manifest profileDigest is not a 0x-prefixed 32-byte hex digest")
	}
	if !manifestDigestPattern.MatchString(m.MetadataDigest) {
		return Manifest{}, fmt.Errorf("auditpkg: manifest metadataDigest is not a 0x-prefixed 32-byte hex digest")
	}

	seen := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		if seen[f.Path] {
			return Manifest{}, fmt.Errorf("auditpkg: manifest.files has duplicate path %q", f.Path)
		}
		seen[f.Path] = true

		if !manifestPathPattern.MatchString(f.Path) {
			return Manifest{}, fmt.Errorf("auditpkg: manifest file path %q does not match ^[A-Za-z0-9._/-]{1,255}$", f.Path)
		}
		if !manifestSHA256Pattern.MatchString(f.SHA256) {
			return Manifest{}, fmt.Errorf("auditpkg: manifest file %q has an invalid sha256", f.Path)
		}
		if f.Size < 0 || f.Size > maxManifestFileSize {
			return Manifest{}, fmt.Errorf("auditpkg: manifest file %q size %d is out of bounds [0,%d]", f.Path, f.Size, maxManifestFileSize)
		}
		if f.MIME == "" {
			return Manifest{}, fmt.Errorf("auditpkg: manifest file %q is missing mime", f.Path)
		}

		if !mandatoryManifestFiles[f.Path] && !strings.HasPrefix(f.Path, "proofs/") {
			return Manifest{}, fmt.Errorf("auditpkg: manifest file %q is neither a mandatory file nor under proofs/", f.Path)
		}
	}
	for name := range mandatoryManifestFiles {
		if !seen[name] {
			return Manifest{}, fmt.Errorf("auditpkg: manifest.files is missing required entry %q", name)
		}
	}

	return m, nil
}
