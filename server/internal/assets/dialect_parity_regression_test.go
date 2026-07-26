package assets

import (
	"strings"
	"testing"
)

// TestDialectRejectsUnusedDefsMember guards a subtle server/signer parity
// case: `{"$defs":{"unused":{"multipleOf":2}}}` must be rejected. A validator
// that only dialect-checks $defs entries reachable through a $ref would let a
// disallowed keyword slip through in an unreferenced entry. The dialect treats
// $defs as a container whose members are all schema objects, each of which
// must itself satisfy the dialect regardless of whether anything $refs it, so
// guardAssetSchema's $defs branch (schema_guard.go) walks every entry
// unconditionally with its own fresh refChain. This test pins that.
func TestDialectRejectsUnusedDefsMember(t *testing.T) {
	err := guardAssetSchema([]byte(`{"$defs":{"unused":{"multipleOf":2}}}`))
	if err == nil {
		t.Fatal("expected an unused $defs member with a disallowed keyword (multipleOf) to be rejected")
	}
	if !strings.Contains(err.Error(), "multipleOf") {
		t.Fatalf("expected rejection to name the offending keyword, got: %v", err)
	}
}

// TestEmptyTypeArrayRejected guards another parity case: `{"type":[]}` is
// rejected by the server's full Draft 2020-12 compiler, but a hand-written
// validator can mistakenly treat an empty type array as "any type" and
// silently accept an arbitrary instance. guardAssetSchema itself does
// not reject an empty type array (validateTypeValue's []any branch has no
// error path for zero elements — an empty array is vacuously "every element
// is a recognized type name"), so parity here depends entirely on
// compileAssetSchema's underlying jsonschema.Compiler rejecting it. Assert
// that explicitly so a library upgrade or refactor that silently starts
// accepting `type: []` is caught here instead of only in production.
func TestEmptyTypeArrayRejected(t *testing.T) {
	raw := []byte(`{"type":[]}`)
	if err := guardAssetSchema(raw); err != nil {
		return // rejected before compile is also an acceptable outcome
	}
	if _, err := compileAssetSchema(raw); err == nil {
		t.Fatal("expected type:[] to be rejected (by the guard or the compiler)")
	}
}
