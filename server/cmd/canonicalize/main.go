// Command canonicalize prints the SHA-256 digest and CIDv1 of the RFC 8785 (JCS)
// canonical form of a JSON document, using the same internal/auditpkg code the
// server uses for Asset Profile and metadata digests. An operator can use it to
// compute the profileDigest that contracts/script/Deploy.s.sol reads from
// PROFILE_DIGEST before the server has seen the profile.
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
