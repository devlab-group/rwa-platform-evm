// Package output writes signed results and Safe owner shares in the formats described by
// shared/schemas/signed-result.schema.json and shared/schemas/safe-signature-share.schema.json
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"
)

// SignedResult is the final mint or burn authorization
type SignedResult struct {
	FormatVersion   string `json:"formatVersion"`
	Auditor         string `json:"auditor"`
	PrimaryType     string `json:"primaryType"`
	TypedDataDigest string `json:"typedDataDigest"`
	Signature       string `json:"signature"`
	SignedAt        string `json:"signedAt"`
}

// SignatureShare holds one owner's signature over the Safe message hash of an attestation
type SignatureShare struct {
	FormatVersion   string `json:"formatVersion"`
	Safe            string `json:"safe"`
	ChainID         string `json:"chainId"`
	Owner           string `json:"owner"`
	PrimaryType     string `json:"primaryType"`
	TypedDataDigest string `json:"typedDataDigest"`
	SafeMessageHash string `json:"safeMessageHash"`
	Signature       string `json:"signature"`
	SignedAt        string `json:"signedAt"`
}

var (
	addressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	digestPattern  = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
	chainIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
	// Accept one EOA signature or concatenated Safe owner signatures
	resultSigPattern = regexp.MustCompile(`^0x([0-9a-fA-F]{130})+$`)
	shareSigPattern  = regexp.MustCompile(`^0x[0-9a-fA-F]{130}$`)
)

// New builds a SignedResult with a UTC RFC 3339 timestamp and checks its field formats
func New(auditor, primaryType, typedDataDigest, signature string) (SignedResult, error) {
	r := SignedResult{
		FormatVersion:   "1.0",
		Auditor:         auditor,
		PrimaryType:     primaryType,
		TypedDataDigest: typedDataDigest,
		Signature:       signature,
		SignedAt:        now(),
	}
	if err := r.Validate(); err != nil {
		return SignedResult{}, err
	}
	return r, nil
}

// Validate checks field formats without verifying the signature
func (r SignedResult) Validate() error {
	if r.FormatVersion != "1.0" {
		return fmt.Errorf("output: formatVersion must be \"1.0\"")
	}
	if !addressPattern.MatchString(r.Auditor) {
		return fmt.Errorf("output: auditor must match ^0x[0-9a-fA-F]{40}$")
	}
	if err := validatePrimaryType(r.PrimaryType); err != nil {
		return err
	}
	if !digestPattern.MatchString(r.TypedDataDigest) {
		return fmt.Errorf("output: typedDataDigest must match ^0x[0-9a-fA-F]{64}$")
	}
	if !resultSigPattern.MatchString(r.Signature) {
		return fmt.Errorf("output: signature must be one or more 65-byte signatures, ^0x([0-9a-fA-F]{130})+$")
	}
	return validateSignedAt(r.SignedAt)
}

// NewShare builds a SignatureShare with a UTC RFC 3339 timestamp and checks its field formats
func NewShare(safe, chainID, owner, primaryType, typedDataDigest, safeMessageHash, signature string) (SignatureShare, error) {
	s := SignatureShare{
		FormatVersion:   "1.0",
		Safe:            safe,
		ChainID:         chainID,
		Owner:           owner,
		PrimaryType:     primaryType,
		TypedDataDigest: typedDataDigest,
		SafeMessageHash: safeMessageHash,
		Signature:       signature,
		SignedAt:        now(),
	}
	if err := s.Validate(); err != nil {
		return SignatureShare{}, err
	}
	return s, nil
}

// Validate checks share field formats without recovering the signing owner
func (s SignatureShare) Validate() error {
	if s.FormatVersion != "1.0" {
		return fmt.Errorf("output: share formatVersion must be \"1.0\"")
	}
	if !addressPattern.MatchString(s.Safe) || !addressPattern.MatchString(s.Owner) {
		return fmt.Errorf("output: share safe and owner must match ^0x[0-9a-fA-F]{40}$")
	}
	if !chainIDPattern.MatchString(s.ChainID) {
		return fmt.Errorf("output: share chainId must be a positive decimal integer")
	}
	if err := validatePrimaryType(s.PrimaryType); err != nil {
		return err
	}
	if !digestPattern.MatchString(s.TypedDataDigest) || !digestPattern.MatchString(s.SafeMessageHash) {
		return fmt.Errorf("output: share typedDataDigest and safeMessageHash must match ^0x[0-9a-fA-F]{64}$")
	}
	if !shareSigPattern.MatchString(s.Signature) {
		return fmt.Errorf("output: share signature must be one 65-byte signature, ^0x[0-9a-fA-F]{130}$")
	}
	return validateSignedAt(s.SignedAt)
}

// ReadShare decodes a share, rejects unknown fields and checks its field formats
func ReadShare(path string) (SignatureShare, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SignatureShare{}, fmt.Errorf("output: reading %s: %w", path, err)
	}
	var s SignatureShare
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return SignatureShare{}, fmt.Errorf("output: parsing %s: %w", path, err)
	}
	if dec.More() {
		return SignatureShare{}, fmt.Errorf("output: %s: trailing data after JSON value", path)
	}
	if err := s.Validate(); err != nil {
		return SignatureShare{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Write checks r and writes indented JSON to path
func Write(path string, r SignedResult) error {
	if err := r.Validate(); err != nil {
		return err
	}
	return writeJSON(path, r)
}

// WriteShare validates s and writes it as indented JSON to path
func WriteShare(path string, s SignatureShare) error {
	if err := s.Validate(); err != nil {
		return err
	}
	return writeJSON(path, s)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("output: marshaling %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("output: writing %s: %w", path, err)
	}
	return nil
}

func validatePrimaryType(primaryType string) error {
	if primaryType != "MintAttestation" && primaryType != "BurnAttestation" {
		return fmt.Errorf("output: primaryType must be MintAttestation or BurnAttestation")
	}
	return nil
}

func validateSignedAt(signedAt string) error {
	if _, err := time.Parse(time.RFC3339, signedAt); err != nil {
		return fmt.Errorf("output: signedAt must be an RFC 3339 date-time: %w", err)
	}
	return nil
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
