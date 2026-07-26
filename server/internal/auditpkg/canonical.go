// Package auditpkg implements the deterministic hashing/CID convention and
// builds/verifies .rwa audit packages. JCS canonicalization uses
// github.com/gowebpki/jcs, the reference Go
// implementation of RFC 8785 by the RFC's authors, so canonical bytes match
// any other conformant implementation (the Go signer, the TS client) without
// reimplementing the ECMAScript number/string serialization rules by hand.
package auditpkg

import (
	"crypto/sha256"
	"fmt"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

// Limits enforced even when a schema omits its own bounds — the frozen
// structural-limits table every implementation shares.
const (
	MaxDocumentBytes = 1 << 20 // 1,048,576 bytes (1 MiB)
	MaxObjectDepth   = 32
	MaxStringLength  = 32768
	MaxArrayLength   = 4096
	MaxObjectKeys    = 4096
)

// CheckLimits validates that raw is a well-formed, size/UTF-8-safe JSON
// document within the platform-wide structural limits (document size,
// nesting depth, string length, and array/object element counts — the
// frozen structural limits), independent of any JSON Schema a caller may
// separately apply.
//
// Exported so a caller that must itself recurse into the document via a
// generic `any` tree or a JSON Schema compiler (server/internal/assets:
// guardAssetSchema, compileAssetSchema, jsonschema.Schema.Validate) can
// enforce these bounds BEFORE that recursion happens, not only as a
// byproduct of a later Canonicalize/digest call. checkLimits' walk is an
// iterative token-stream scan (an explicit stack, not Go call recursion),
// so it fails closed on adversarial depth/size instead of itself risking a
// stack-exhaustion DoS.
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

// Canonicalize validates that raw is well-formed, size/UTF-8-safe JSON and
// returns its RFC 8785 canonical byte form. It rejects duplicate object keys
// (via the underlying jcs.Transform) and depth/size/length limits before
// transforming, since jcs.Transform alone does not enforce platform limits.
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

// Digest returns SHA-256(canonicalBytes). Callers MUST pass
// already-canonicalized bytes (the output of Canonicalize), not arbitrary
// JSON, so the digest is reproducible.
func Digest(canonicalBytes []byte) [32]byte {
	return sha256.Sum256(canonicalBytes)
}

// CIDv1Raw builds an IPFS CIDv1 using codec `raw` and multihash `sha2-256`
// from the same canonical bytes used for Digest.
func CIDv1Raw(canonicalBytes []byte) (string, error) {
	mh, err := multihash.Sum(canonicalBytes, multihash.SHA2_256, -1)
	if err != nil {
		return "", fmt.Errorf("auditpkg: multihash: %w", err)
	}
	c := cid.NewCidV1(cid.Raw, mh)
	return c.String(), nil
}

// CanonicalizeAndDigest is a convenience wrapper running Canonicalize,
// Digest, and CIDv1Raw together, returning the canonical bytes, the 32-byte
// digest, and the CID string.
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

// checkLimits walks raw JSON tokens with encoding/json's tokenizer to bound
// nesting depth, string length, and array/object element counts before
// canonicalization, independent of whatever bounds (if any) a caller's JSON
// Schema declares.
func checkLimits(raw []byte) error {
	dec := newLimitDecoder(raw)
	return dec.walk()
}
