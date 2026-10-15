// Package cid builds raw-codec (0x55), sha2-256 CIDv1 identifiers from canonical JSON
// bytes. Their SHA-256 payload equals profileDigest/metadataDigest, so either digest
// alone can reconstruct its CID.
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

// base32Lower uses RFC 4648 lowercase base32 without padding for multibase "b"
var base32Lower = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// Digest returns SHA-256(canonicalBytes)
func Digest(canonicalBytes []byte) [32]byte {
	return sha256.Sum256(canonicalBytes)
}

// FromDigest encodes a raw-codec sha2-256 digest (such as profileDigest/metadataDigest)
// as CIDv1 with lowercase base32 multibase "b"
func FromDigest(digest [32]byte) string {
	buf := make([]byte, 0, 4+len(digest))
	buf = append(buf, version, codecRaw, mhSHA2256, mhLength)
	buf = append(buf, digest[:]...)
	return "b" + base32Lower.EncodeToString(buf)
}

// FromCanonical returns canonicalBytes' SHA-256 digest and CIDv1 string
func FromCanonical(canonicalBytes []byte) (string, [32]byte) {
	d := Digest(canonicalBytes)
	return FromDigest(d), d
}
