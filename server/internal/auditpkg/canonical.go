// Package auditpkg canonicalizes, hashes and content-addresses profile and
// metadata documents, and builds and verifies .rwa audit packages.
// Canonicalization uses github.com/gowebpki/jcs, a fork of the reference Go
// implementation of RFC 8785, so canonical bytes match other conformant
// implementations without reimplementing the ECMAScript number and string
// serialization rules.
package auditpkg

import (
	"crypto/sha256"
	"fmt"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

// The structural limits from docs/spec/asset-profile.md ("Structural limits").
// They apply even when a schema declares no bounds of its own.
// MaxStringLength counts UTF-8 bytes and covers object keys as well as values.
const (
	MaxDocumentBytes = 1 << 20 // 1,048,576 bytes (1 MiB)
	MaxObjectDepth   = 32
	MaxStringLength  = 32768
	MaxArrayLength   = 4096
	MaxObjectKeys    = 4096
)

// CheckLimits rejects raw unless it is non-empty, valid UTF-8 and well-formed
// JSON within the structural limits: document size, nesting depth, string
// length, array length and object key count. The limits apply regardless of
// any JSON Schema the caller also uses.
//
// Callers that recurse into an untrusted document, such as the asset schema
// guard, compiler and validator in internal/assets, call it first. The scan
// reads tokens with an explicit stack instead of recursing, so deeply nested
// input cannot exhaust the Go stack.
func CheckLimits(raw []byte) error {
	if len(raw) == 0 {
		return fmt.Errorf("auditpkg: empty document")
	}
	if len(raw) > MaxDocumentBytes {
		return fmt.Errorf("auditpkg: document exceeds %d bytes", MaxDocumentBytes)
	}
	if !utf8.Valid(raw) {
		return fmt.Errorf("auditpkg: document is not valid UTF-8")
	}
	return checkLimits(raw)
}

// Canonicalize returns the RFC 8785 canonical form of raw. It runs CheckLimits
// first because jcs.Transform does not enforce the structural limits, and it
// rejects duplicate object keys through jcs.Transform.
func Canonicalize(raw []byte) ([]byte, error) {
	if err := CheckLimits(raw); err != nil {
		return nil, err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("auditpkg: canonicalization failed: %w", err)
	}
	return canonical, nil
}

// Digest returns SHA-256(canonicalBytes). Callers must pass the output of
// Canonicalize, not arbitrary JSON, so the digest is reproducible.
func Digest(canonicalBytes []byte) [32]byte {
	return sha256.Sum256(canonicalBytes)
}

// CIDv1Raw returns the CIDv1 string (raw codec, sha2-256 multihash, base32)
// of canonicalBytes, the same bytes Digest hashes
func CIDv1Raw(canonicalBytes []byte) (string, error) {
	mh, err := multihash.Sum(canonicalBytes, multihash.SHA2_256, -1)
	if err != nil {
		return "", fmt.Errorf("auditpkg: multihash: %w", err)
	}
	c := cid.NewCidV1(cid.Raw, mh)
	return c.String(), nil
}

// CanonicalizeAndDigest runs Canonicalize, Digest and CIDv1Raw on raw and
// returns the canonical bytes, the digest and the CID string
func CanonicalizeAndDigest(raw []byte) (canonical []byte, digest [32]byte, cidStr string, err error) {
	canonical, err = Canonicalize(raw)
	if err != nil {
		return nil, [32]byte{}, "", err
	}
	digest = Digest(canonical)
	cidStr, err = CIDv1Raw(canonical)
	if err != nil {
		return nil, [32]byte{}, "", err
	}
	return canonical, digest, cidStr, nil
}

// checkLimits walks raw with the encoding/json tokenizer and enforces the
// nesting depth, string length, and array and object size limits
func checkLimits(raw []byte) error {
	dec := newLimitDecoder(raw)
	return dec.walk()
}
