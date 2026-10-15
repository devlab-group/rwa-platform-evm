package assets

import (
	"strings"
	"testing"
)

// TestDialectRejectsUnusedDefsMember rejects {"$defs":{"unused":{"multipleOf":2}}}.
// guardAssetSchema must check every definition, including unreferenced ones, for
// forbidden keywords.
func TestDialectRejectsUnusedDefsMember(t *testing.T) {
	err := guardAssetSchema([]byte(`{"$defs":{"unused":{"multipleOf":2}}}`))
	if err == nil {
		t.Fatal("expected an unused $defs member with a disallowed keyword (multipleOf) to be rejected")
	}
	if !strings.Contains(err.Error(), "multipleOf") {
		t.Fatalf("expected rejection to name the offending keyword, got: %v", err)
	}
}

// TestEmptyTypeArrayRejected keeps {"type":[]} invalid across library upgrades. The
// guard permits it, but compileAssetSchema must reject it rather than allow any type.
func TestEmptyTypeArrayRejected(t *testing.T) {
	raw := []byte(`{"type":[]}`)
	if err := guardAssetSchema(raw); err != nil {
		return // rejected before compile is also an acceptable outcome
	}
	if _, err := compileAssetSchema(raw); err == nil {
		t.Fatal("expected type:[] to be rejected (by the guard or the compiler)")
	}
}
