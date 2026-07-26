// Package output writes signed-result.json, matching
// shared/schemas/signed-result.schema.json exactly.
package output

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"
)

// SignedResult is the final signer output.
type SignedResult struct {
	FormatVersion   string `json:"formatVersion"`
	Auditor         string `json:"auditor"`
	PrimaryType     string `json:"primaryType"`
	TypedDataDigest string `json:"typedDataDigest"`
	Signature       string `json:"signature"`
	SignedAt        string `json:"signedAt"`
}

var (
	auditorPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	digestPattern  = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
	sigPattern     = regexp.MustCompile(`^0x[0-9a-fA-F]{130}$`)
)

// New builds a SignedResult with SignedAt set to now (UTC, RFC 3339), and
// validates every field against shared/schemas/signed-result.schema.json
// before returning it.
func New(auditor, primaryType, typedDataDigest, signature string) (SignedResult, error) {
	r := SignedResult{
		FormatVersion:   "1.0",
		Auditor:         auditor,
		PrimaryType:     primaryType,
		TypedDataDigest: typedDataDigest,
		Signature:       signature,
		SignedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	if err := r.Validate(); err != nil {
		return SignedResult{}, err
	}
	return r, nil
}

// Validate checks r against shared/schemas/signed-result.schema.json.
func (r SignedResult) Validate() error {
	if r.FormatVersion != "1.0" {
		return fmt.Errorf("output: formatVersion must be \"1.0\"")
	}
	if !auditorPattern.MatchString(r.Auditor) {
		return fmt.Errorf("output: auditor must match ^0x[0-9a-fA-F]{40}$")
	}
	if r.PrimaryType != "MintAttestation" && r.PrimaryType != "BurnAttestation" {
		return fmt.Errorf("output: primaryType must be MintAttestation or BurnAttestation")
	}
	if !digestPattern.MatchString(r.TypedDataDigest) {
		return fmt.Errorf("output: typedDataDigest must match ^0x[0-9a-fA-F]{64}$")
	}
	if !sigPattern.MatchString(r.Signature) {
		return fmt.Errorf("output: signature must match ^0x[0-9a-fA-F]{130}$")
	}
	if _, err := time.Parse(time.RFC3339, r.SignedAt); err != nil {
		return fmt.Errorf("output: signedAt must be an RFC 3339 date-time: %w", err)
	}
	return nil
}

// Write validates r and writes it as indented JSON to path.
func Write(path string, r SignedResult) error {
	if err := r.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("output: marshaling signed-result.json: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("output: writing %s: %w", path, err)
	}
	return nil
}
