// Command canonicalize prints a JSON document's RFC 8785 JCS SHA-256 and CIDv1 using
// internal/auditpkg, matching server profile and metadata digests. Use it to compute
// Deploy.s.sol's PROFILE_DIGEST before profile creation.
//
// Usage: go run ./cmd/canonicalize <path-to-json-file>
package main

import (
	"fmt"
	"os"

	"github.com/rwa-platform/server/internal/auditpkg"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: canonicalize <path-to-json-file>")
		os.Exit(1)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "canonicalize: reading %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	_, digest, cid, err := auditpkg.CanonicalizeAndDigest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "canonicalize: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("digest=0x%x\n", digest)
	fmt.Printf("cid=%s\n", cid)
}
