package auditpkg

import (
	"bytes"
	"testing"
)

// TestBuildPackageIsDeterministic checks that identical inputs produce
// byte-identical packages, with no timestamp or other varying state.
// RecordService.BuildPackage in internal/assets rebuilds a package from the
// record's stored fields on every download and does not keep the package
// bytes, so the rebuilt bytes must equal what was built before.
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
