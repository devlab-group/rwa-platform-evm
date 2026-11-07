package jsonschema

import (
	"fmt"
	"strings"
	"testing"
	"time"
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

// Reject unsupported constraints such as multipleOf instead of ignoring them, following
// docs/spec/profile-schema-dialect.md and
// shared/vectors/schema-dialect-conformance.json
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

// Reject all $ref siblings; their Draft 2020-12 validation semantics are not
// implemented
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

// Reject direct/indirect $ref cycles before recursive compilation can overflow the
// signer stack
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

// Bound acyclic depth through both nested schemas and $ref chains
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

// Validate conforming and nonconforming gold-bar example assets
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

// diamondSchema has levels definitions that each refer twice to the next, so a full
// expansion visits 2^levels nodes
func diamondSchema(levels int) string {
	var defs []string
	for i := 0; i < levels-1; i++ {
		defs = append(defs, fmt.Sprintf(`"d%d":{"properties":{"a":{"$ref":"#/$defs/d%d"},"b":{"$ref":"#/$defs/d%d"}}}`, i, i+1, i+1))
	}
	defs = append(defs, fmt.Sprintf(`"d%d":{"type":"string"}`, levels-1))
	return `{"$defs":{` + strings.Join(defs, ",") + `},"properties":{"a":{"$ref":"#/$defs/d0"}}}`
}

// A fan-out of shared definitions stops at the node cap, within the test's time budget
func TestDialectDiamondRefsStopAtNodeCap(t *testing.T) {
	start := time.Now()
	_, err := CompileBytes([]byte(diamondSchema(30)))
	if err == nil || !strings.Contains(err.Error(), "maximum node count of 10000") {
		t.Fatalf("err = %v, want the node cap", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v to refuse the schema", d)
	}
}

// A definition nested in a $ref target that refers back to the target is a cycle
func TestDialectCycleThroughDefsOfTarget(t *testing.T) {
	_, err := CompileBytes([]byte(`{"$defs":{"t":{"$defs":{"x":{"$ref":"#/$defs/t"}}}}}`))
	if err == nil || !strings.Contains(err.Error(), "cyclic $ref") {
		t.Fatalf("err = %v, want a cyclic $ref error", err)
	}
}
