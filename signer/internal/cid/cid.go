// Package cid computes IPFS CIDv1 identifiers (codec raw 0x55, multihash sha2-256)
// from RFC 8785 canonical JSON bytes. The multihash payload is the same SHA-256
// digest used as profileDigest or metadataDigest, so the CID can be rebuilt from
// that digest alone.
package cid

import (
	"crypto/sha256"
	"encoding/base32"
)

const (
	version   = 0x01 // CIDv1
	codecRaw  = 0x55 // multicodec "raw"
	mhSHA2256 = 0x12 // multicodec "sha2-256"
	mhLength  = 0x20 // 32-byte digest
)

// base32Lower is the RFC 4648 base32 alphabet in lowercase without padding, as used
// by the "b" multibase prefix
var base32Lower = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// Digest returns SHA-256(canonicalBytes)
func Digest(canonicalBytes []byte) [32]byte {
	return sha256.Sum256(canonicalBytes)
}

// FromDigest builds the CIDv1 string (multibase "b", base32 lowercase) for a raw-codec
// sha2-256 digest computed elsewhere, such as a profileDigest or metadataDigest
func FromDigest(digest [32]byte) string {
	buf := make([]byte, 0, 4+len(digest))
	buf = append(buf, version, codecRaw, mhSHA2256, mhLength)
	buf = append(buf, digest[:]...)
	return "b" + base32Lower.EncodeToString(buf)
}

// FromCanonical computes the SHA-256 digest of canonicalBytes and returns both the
// CIDv1 string and the digest
func FromCanonical(canonicalBytes []byte) (string, [32]byte) {
	d := Digest(canonicalBytes)
	return FromDigest(d), d
}
