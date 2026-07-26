package auditpkg

import (
	"bytes"
	"testing"
)

// TestBuildPackageIsDeterministic proves BuildPackage is a pure function of
// its inputs — no embedded wall-clock timestamp or other non-deterministic
// state — the property the package-reconstruction path relies on:
// server/internal/assets.RecordService.BuildPackage reconstructs the .rwa
// package from FROZEN inputs (auditor/nonce/expiry/digests captured once,
// at first build) rather than retaining the package's raw bytes anywhere
// (including IPFS — package bytes are deliberately kept out of IPFS per
// the platform's existing storage policy). That reconstruction is only
// safe to treat as "the same package the auditor actually signed" if two
// calls with identical inputs always produce byte-identical output, which
// this test pins.
func TestBuildPackageIsDeterministic(t *testing.T) {
	td := TypedDataDoc{}
	b1, h1, err := BuildPackage("MintAttestation", [32]byte{1}, [32]byte{2}, []byte(`{"a":1}`), []byte(`{"b":2}`), td, nil)
	if err != nil {
		t.Fatal(err)
	}
	b2, h2, err := BuildPackage("MintAttestation", [32]byte{1}, [32]byte{2}, []byte(`{"a":1}`), []byte(`{"b":2}`), td, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash mismatch across identical calls: %s vs %s", h1, h2)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatalf("byte mismatch across identical calls, len %d vs %d", len(b1), len(b2))
	}
}
