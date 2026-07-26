package jsonschema

import (
	"fmt"
	"strings"
	"testing"
)

func mustCompile(t *testing.T, raw string) *Schema {
	t.Helper()
	s, err := CompileBytes([]byte(raw))
	if err != nil {
		t.Fatalf("CompileBytes: %v", err)
	}
	return s
}

func TestCompile_RejectsForbiddenKeywords(t *testing.T) {
	for _, kw := range []string{
		`"oneOf":[{"type":"string"}]`,
		`"anyOf":[{"type":"string"}]`,
		`"allOf":[{"type":"string"}]`,
		`"not":{"type":"string"}`,
		`"if":{"type":"string"}`,
		`"$dynamicRef":"#foo"`,
		`"unevaluatedProperties":false`,
		`"unevaluatedItems":false`,
	} {
		raw := `{"type":"object",` + kw + `}`
		if _, err := CompileBytes([]byte(raw)); err == nil {
			t.Errorf("expected rejection for %s, got nil error", kw)
		}
	}
}

func TestCompile_RejectsRemoteRef(t *testing.T) {
	_, err := CompileBytes([]byte(`{"$ref":"https://example.com/schema.json"}`))
	if err == nil {
		t.Fatal("expected rejection of remote $ref")
	}
}

func TestCompile_ResolvesLocalRef(t *testing.T) {
	raw := `{
		"$defs": {"pos": {"type": "integer", "minimum": 0}},
		"type": "object",
		"properties": {"n": {"$ref": "#/$defs/pos"}}
	}`
	s := mustCompile(t, raw)
	if err := s.ValidateBytes([]byte(`{"n":5}`)); err != nil {
		t.Errorf("valid document rejected: %v", err)
	}
	if err := s.ValidateBytes([]byte(`{"n":-1}`)); err == nil {
		t.Error("expected rejection of negative value through $ref")
	}
}

func TestValidate_RequiredAndAdditionalProperties(t *testing.T) {
	s := mustCompile(t, `{
		"type":"object",
		"additionalProperties": false,
		"required": ["a"],
		"properties": {"a": {"type":"string"}}
	}`)
	if err := s.ValidateBytes([]byte(`{"a":"x"}`)); err != nil {
		t.Errorf("valid document rejected: %v", err)
	}
	if err := s.ValidateBytes([]byte(`{}`)); err == nil {
		t.Error("expected rejection of missing required property")
	}
	if err := s.ValidateBytes([]byte(`{"a":"x","b":1}`)); err == nil {
		t.Error("expected rejection of additional property")
	}
}

func TestValidate_StringConstraints(t *testing.T) {
	s := mustCompile(t, `{"type":"string","minLength":2,"maxLength":4,"pattern":"^[a-z]+$"}`)
	if err := s.ValidateBytes([]byte(`"abc"`)); err != nil {
		t.Errorf("valid string rejected: %v", err)
	}
	if err := s.ValidateBytes([]byte(`"a"`)); err == nil {
		t.Error("expected rejection: too short")
	}
	if err := s.ValidateBytes([]byte(`"abcde"`)); err == nil {
		t.Error("expected rejection: too long")
	}
	if err := s.ValidateBytes([]byte(`"ABC"`)); err == nil {
		t.Error("expected rejection: pattern mismatch")
	}
}

func TestValidate_IntegerBounds(t *testing.T) {
	s := mustCompile(t, `{"type":"integer","minimum":0,"maximum":36}`)
	if err := s.ValidateBytes([]byte(`18`)); err != nil {
		t.Errorf("valid integer rejected: %v", err)
	}
	if err := s.ValidateBytes([]byte(`-1`)); err == nil {
		t.Error("expected rejection: below minimum")
	}
	if err := s.ValidateBytes([]byte(`37`)); err == nil {
		t.Error("expected rejection: above maximum")
	}
	if err := s.ValidateBytes([]byte(`1.5`)); err == nil {
		t.Error("expected rejection: not an integer")
	}
}

func TestValidate_EnumAndConst(t *testing.T) {
	s := mustCompile(t, `{"enum":["a","b","c"]}`)
	if err := s.ValidateBytes([]byte(`"b"`)); err != nil {
		t.Errorf("valid enum value rejected: %v", err)
	}
	if err := s.ValidateBytes([]byte(`"z"`)); err == nil {
		t.Error("expected rejection: not in enum")
	}

	c := mustCompile(t, `{"const":"1.0"}`)
	if err := c.ValidateBytes([]byte(`"1.0"`)); err != nil {
		t.Errorf("valid const value rejected: %v", err)
	}
	if err := c.ValidateBytes([]byte(`"2.0"`)); err == nil {
		t.Error("expected rejection: does not match const")
	}
}

func TestValidate_ArrayConstraints(t *testing.T) {
	s := mustCompile(t, `{"type":"array","items":{"type":"integer"},"minItems":1,"maxItems":3,"uniqueItems":true}`)
	if err := s.ValidateBytes([]byte(`[1,2,3]`)); err != nil {
		t.Errorf("valid array rejected: %v", err)
	}
	if err := s.ValidateBytes([]byte(`[]`)); err == nil {
		t.Error("expected rejection: below minItems")
	}
	if err := s.ValidateBytes([]byte(`[1,2,3,4]`)); err == nil {
		t.Error("expected rejection: above maxItems")
	}
	if err := s.ValidateBytes([]byte(`[1,1]`)); err == nil {
		t.Error("expected rejection: duplicate items")
	}
	if err := s.ValidateBytes([]byte(`["x"]`)); err == nil {
		t.Error("expected rejection: wrong item type")
	}
}

// TestCompile_RejectsUnknownKeywordsByAllowlist pins the allowlist behavior:
// rejecting a keyword outright, not silently ignoring it. A blacklist-only
// approach would let a schema using e.g. multipleOf compile and simply never
// enforce it. Compile must reject anything outside the explicit allowlist,
// including valid Draft 2020-12 keywords this subset engine does not
// implement. The allowlist follows the dialect in
// docs/spec/profile-schema-dialect.md and
// shared/vectors/schema-dialect-conformance.json.
func TestCompile_RejectsUnknownKeywordsByAllowlist(t *testing.T) {
	for _, kw := range []string{
		`"multipleOf":2`,
		`"minProperties":1`,
		`"maxProperties":5`,
		`"patternProperties":{"^a":{"type":"string"}}`,
		`"dependentRequired":{"a":["b"]}`,
		`"contains":{"type":"string"}`,
		`"format":"date-time"`,
		`"propertyNames":{"type":"string"}`,
		`"prefixItems":[{"type":"string"}]`,
	} {
		raw := `{"type":"object",` + kw + `}`
		if _, err := CompileBytes([]byte(raw)); err == nil {
			t.Errorf("expected rejection for unknown keyword %s, got nil error (silently ignored)", kw)
		}
	}
}

// TestCompile_RejectsRefSiblings: Draft 2020-12 gives $ref siblings
// independent validation force, which this engine does not implement, so a
// $ref with any sibling keyword must be rejected rather than silently
// discarding the sibling.
func TestCompile_RejectsRefSiblings(t *testing.T) {
	raw := `{
		"$defs": {"pos": {"type": "integer", "minimum": 0}},
		"type": "object",
		"properties": {"n": {"$ref": "#/$defs/pos", "description": "a positive int"}}
	}`
	if _, err := CompileBytes([]byte(raw)); err == nil {
		t.Fatal("expected rejection of $ref with a sibling keyword")
	}
}

// TestCompile_RejectsDirectRefCycle and TestCompile_RejectsIndirectRefCycle
// guard the $ref cycle tracking. Without it a self-referential schema would
// recurse until the process crashed with a stack overflow -- a malicious
// package carrying such a schema is a local denial of service against an
// auditor opening it.
func TestCompile_RejectsDirectRefCycle(t *testing.T) {
	raw := `{"$defs": {"a": {"$ref": "#/$defs/a"}}, "$ref": "#/$defs/a"}`
	if _, err := CompileBytes([]byte(raw)); err == nil {
		t.Fatal("expected rejection of a directly self-referential $ref")
	}
}

func TestCompile_RejectsIndirectRefCycle(t *testing.T) {
	raw := `{
		"$defs": {
			"a": {"$ref": "#/$defs/b"},
			"b": {"$ref": "#/$defs/a"}
		},
		"$ref": "#/$defs/a"
	}`
	if _, err := CompileBytes([]byte(raw)); err == nil {
		t.Fatal("expected rejection of a mutually-cyclic $ref pair")
	}
}

// TestCompile_RejectsExcessiveDepth covers the depth limit for non-cyclic
// but pathologically deep schemas (whether via nesting or via a long $ref
// chain).
func TestCompile_RejectsExcessiveDepth(t *testing.T) {
	defs := &strings.Builder{}
	defs.WriteString(`{"$defs":{`)
	const n = maxSchemaDepth + 10
	for i := 0; i < n; i++ {
		if i > 0 {
			defs.WriteString(",")
		}
		if i == n-1 {
			fmt.Fprintf(defs, `"d%d":{"type":"integer"}`, i)
		} else {
			fmt.Fprintf(defs, `"d%d":{"$ref":"#/$defs/d%d"}`, i, i+1)
		}
	}
	defs.WriteString(`},"$ref":"#/$defs/d0"}`)

	if _, err := CompileBytes([]byte(defs.String())); err == nil {
		t.Fatal("expected rejection of an excessively deep (non-cyclic) $ref chain")
	}
}

// TestValidate_ArchitectureGoldBarExample validates the gold-bar assetSchema
// example against both a conforming and a non-conforming asset record.
func TestValidate_ArchitectureGoldBarExample(t *testing.T) {
	s := mustCompile(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"additionalProperties": false,
		"required": ["serialNumber", "weightGrams", "purity"],
		"properties": {
			"serialNumber": { "type": "string", "minLength": 1 },
			"weightGrams": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]+)?$" },
			"purity": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]+)?$" }
		}
	}`)

	good := `{"serialNumber":"12345","weightGrams":"1000","purity":"999.9"}`
	if err := s.ValidateBytes([]byte(good)); err != nil {
		t.Errorf("valid asset record rejected: %v", err)
	}

	bad := `{"serialNumber":"12345","weightGrams":1000,"purity":"999.9"}`
	if err := s.ValidateBytes([]byte(bad)); err == nil {
		t.Error("expected rejection: weightGrams must be a string, not a JSON number")
	}

	missing := `{"serialNumber":"12345","purity":"999.9"}`
	if err := s.ValidateBytes([]byte(missing)); err == nil {
		t.Error("expected rejection: missing required weightGrams")
	}
}
