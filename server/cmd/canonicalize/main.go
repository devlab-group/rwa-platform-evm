// Command canonicalize prints the RFC 8785 JCS canonical bytes' SHA-256
// digest (and CIDv1) of a JSON document, using the exact same
// internal/auditpkg logic the server uses to compute an Asset Profile's or a
// metadata record's digest. It exists so operators/tooling (notably
// server/e2e/run_e2e.sh) can pre-compute a profileDigest to hand to
// `forge script script/Deploy.s.sol` as PROFILE_DIGEST *before* the server
// has ever seen the profile, without duplicating the canonicalization logic
// by hand.
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
