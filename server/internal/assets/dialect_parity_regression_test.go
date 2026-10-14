package assets

import (
	"strings"
	"testing"
)

// TestDialectRejectsUnusedDefsMember checks that `{"$defs":{"unused":{"multipleOf":2}}}`
// is rejected. Every $defs member must satisfy the dialect even when no $ref
// reaches it, so guardAssetSchema walks all of them. A validator that checked
// only referenced entries would let a disallowed keyword through.
func TestDialectRejectsUnusedDefsMember(t *testing.T) {
	err := guardAssetSchema([]byte(`{"$defs":{"unused":{"multipleOf":2}}}`))
	if err == nil {
		t.Fatal("expected an unused $defs member with a disallowed keyword (multipleOf) to be rejected")
	}
	if !strings.Contains(err.Error(), "multipleOf") {
		t.Fatalf("expected rejection to name the offending keyword, got: %v", err)
	}
}

// TestEmptyTypeArrayRejected checks that `{"type":[]}` is rejected. A validator
// can treat an empty type array as "any type" and accept every instance.
// guardAssetSchema accepts an empty array, because validateTypeValue has no error
// path for zero elements, so the rejection comes from the jsonschema compiler in
// compileAssetSchema. The test fails if a library upgrade starts accepting it.
func TestEmptyTypeArrayRejected(t *testing.T) {
	raw := []byte(`{"type":[]}`)
	if err := guardAssetSchema(raw); err != nil {
		return // rejected before compile is also an acceptable outcome
	}
	if _, err := compileAssetSchema(raw); err == nil {
		t.Fatal("expected type:[] to be rejected (by the guard or the compiler)")
	}
}
