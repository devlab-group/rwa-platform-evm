package assets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Enforce rwa-profile-assetSchema/1 (docs/spec/profile-schema-dialect.md) before
// compilation so server and signer reject unsupported keywords consistently.
// TestDialectConformance checks shared/vectors/schema-dialect-conformance.json.

// allowedValidationKeywords are the dialect's validation and applicator keywords
var allowedValidationKeywords = map[string]bool{
	"type":                 true,
	"properties":           true,
	"required":             true,
	"additionalProperties": true,
	"items":                true,
	"$ref":                 true,
	"enum":                 true,
	"const":                true,
	"minLength":            true,
	"maxLength":            true,
	"pattern":              true,
	"minimum":              true,
	"maximum":              true,
	"exclusiveMinimum":     true,
	"exclusiveMaximum":     true,
	"minItems":             true,
	"maxItems":             true,
	"uniqueItems":          true,
}

// allowedAnnotationKeywords are accepted and have no validation effect
var allowedAnnotationKeywords = map[string]bool{
	"title":       true,
	"description": true,
	"default":     true,
	"examples":    true,
	"$comment":    true,
	"$defs":       true,
	"$id":         true,
	"$schema":     true,
	"deprecated":  true,
	"readOnly":    true,
	"writeOnly":   true,
}

// enum, const, default and examples hold data, not schemas, so walkSchema never descends
// into them (a const of {"oneOf": 1} does not use the oneOf keyword)

// allowedTypeValues are the primitive type names the dialect accepts in "type"
var allowedTypeValues = map[string]bool{
	"object":  true,
	"array":   true,
	"string":  true,
	"integer": true,
	"number":  true,
	"boolean": true,
	"null":    true,
}

// maxSchemaDepth bounds nesting plus $ref hops; maxSchemaNodes bounds total visits,
// counting each expansion again. The signer shares these limits so repeated references
// cannot trigger unbounded expansion in either validator.
const (
	maxSchemaDepth = 100
	maxSchemaNodes = 10000
)

// lengthKeywords take a non-negative integer written without a fraction or exponent, as
// the signer reads them; numberKeywords take any number
var (
	lengthKeywords = []string{"minLength", "maxLength", "minItems", "maxItems"}
	numberKeywords = []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"}
)

// guardAssetSchema checks rwa-profile-assetSchema/1 before compilation: allowed schema
// keys and type values, plus $ref locality, sole-member placement, resolvability and
// absence of cycles
func guardAssetSchema(raw json.RawMessage) error {
	_, err := prepareAssetSchema(raw)
	return err
}

// prepareAssetSchema checks the dialect and removes $schema and $id from schema positions.
// This keeps the compiler on the fixed draft and resolves local pointers from the document
// root rather than a subschema's $id.
func prepareAssetSchema(raw json.RawMessage) (json.RawMessage, error) {
	// Reject numbers that overflow float64 before canonicalization
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("assetSchema is not valid JSON: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("assetSchema is not valid JSON: %w", err)
	}
	g := &schemaGuard{root: root, visiting: map[string]bool{}}
	if err := g.walkSchema(root, "$", 0); err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

// schemaGuard holds the parsed document root, against which $ref pointers resolve
type schemaGuard struct {
	root any
	// visiting holds the $ref strings on the current path, across $defs as well, so a
	// definition that refers back to the target being expanded is a cycle
	visiting map[string]bool
	nodes    int
}

// walkSchema checks node at path, depth schema levels (nesting plus $ref hops) from the root
func (g *schemaGuard) walkSchema(node any, path string, depth int) error {
	if depth > maxSchemaDepth {
		return fmt.Errorf("assetSchema at %s exceeds the maximum nesting/$ref-chain depth of %d", path, maxSchemaDepth)
	}
	g.nodes++
	if g.nodes > maxSchemaNodes {
		return fmt.Errorf("assetSchema at %s exceeds the maximum node count of %d", path, maxSchemaNodes)
	}
	obj, ok := node.(map[string]any)
	if !ok {
		return fmt.Errorf("assetSchema at %s must be an object", path)
	}

	for k := range obj {
		if !allowedValidationKeywords[k] && !allowedAnnotationKeywords[k] {
			return fmt.Errorf("assetSchema keyword %q is not allowed at %s (closed dialect rwa-profile-assetSchema/1)", k, path)
		}
	}

	if refRaw, hasRef := obj["$ref"]; hasRef {
		if len(obj) != 1 {
			return fmt.Errorf("assetSchema $ref at %s must be the sole member of its object", path)
		}
		ref, ok := refRaw.(string)
		if !ok || !(ref == "#" || strings.HasPrefix(ref, "#/")) {
			return fmt.Errorf(`assetSchema $ref at %s must be a local JSON pointer ("#" or "#/...")`, path)
		}
		// the compiler percent-decodes the fragment, so a pointer is taken literally here
		// and may not contain '%'
		if strings.Contains(ref, "%") {
			return fmt.Errorf("assetSchema $ref at %s must not contain '%%'", path)
		}
		if g.visiting[ref] {
			return fmt.Errorf("assetSchema $ref cycle detected at %s (%s)", path, ref)
		}
		target, err := resolvePointer(g.root, ref)
		if err != nil {
			return fmt.Errorf("assetSchema $ref %q at %s: %w", ref, path, err)
		}
		g.visiting[ref] = true
		err = g.walkSchema(target, path+"->"+ref, depth+1)
		delete(g.visiting, ref)
		return err
	}

	delete(obj, "$schema")
	delete(obj, "$id")

	if typeRaw, ok := obj["type"]; ok {
		if err := validateTypeValue(typeRaw, path); err != nil {
			return err
		}
	}
	if reqRaw, ok := obj["required"]; ok {
		items, ok := reqRaw.([]any)
		if !ok {
			return fmt.Errorf("assetSchema required at %s must be an array of strings", path)
		}
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("assetSchema required at %s must be an array of strings", path)
			}
		}
	}
	if enumRaw, ok := obj["enum"]; ok {
		if items, ok := enumRaw.([]any); !ok || len(items) == 0 {
			return fmt.Errorf("assetSchema enum at %s must be a non-empty array", path)
		}
	}
	for _, kw := range lengthKeywords {
		if v, ok := obj[kw]; ok {
			n, isNum := v.(json.Number)
			if i, err := strconv.Atoi(string(n)); !isNum || err != nil || i < 0 {
				return fmt.Errorf("assetSchema %s at %s must be a non-negative integer written without a fraction or exponent", kw, path)
			}
		}
	}
	for _, kw := range numberKeywords {
		if v, ok := obj[kw]; ok {
			n, isNum := v.(json.Number)
			if _, ok := new(big.Rat).SetString(string(n)); !isNum || !ok {
				return fmt.Errorf("assetSchema %s at %s must be a number", kw, path)
			}
		}
	}
	if propsRaw, ok := obj["properties"]; ok {
		props, ok := propsRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("assetSchema properties at %s must be an object", path)
		}
		for name, sub := range props {
			if err := g.walkSchema(sub, path+".properties."+name, depth+1); err != nil {
				return err
			}
		}
	}
	if apRaw, ok := obj["additionalProperties"]; ok {
		if _, isBool := apRaw.(bool); !isBool {
			if err := g.walkSchema(apRaw, path+".additionalProperties", depth+1); err != nil {
				return err
			}
		}
	}
	if itemsRaw, ok := obj["items"]; ok {
		if err := g.walkSchema(itemsRaw, path+".items", depth+1); err != nil {
			return err
		}
	}
	if defsRaw, ok := obj["$defs"]; ok {
		defs, ok := defsRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("assetSchema $defs at %s must be an object", path)
		}
		// every $defs entry must satisfy the dialect even when nothing refers to it
		for name, sub := range defs {
			if err := g.walkSchema(sub, path+".$defs."+name, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolvePointer resolves a local "#" or "#/a/b/c" JSON pointer against root as
// RFC 6901 describes, decoding ~1 to "/" and ~0 to "~"
func resolvePointer(root any, ref string) (any, error) {
	if ref == "#" {
		return root, nil
	}
	cur := root
	for _, tok := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		tok = strings.ReplaceAll(tok, "~1", "/")
		tok = strings.ReplaceAll(tok, "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("does not resolve within the document (segment %q)", tok)
		}
		v, ok := m[tok]
		if !ok {
			return nil, fmt.Errorf("unresolved segment %q", tok)
		}
		cur = v
	}
	return cur, nil
}

// validateTypeValue checks that a "type" value is a primitive type name or an
// array of them
func validateTypeValue(v any, path string) error {
	switch t := v.(type) {
	case string:
		if !allowedTypeValues[t] {
			return fmt.Errorf("assetSchema type %q at %s is not a recognized JSON Schema type", t, path)
		}
	case []any:
		if len(t) == 0 {
			return fmt.Errorf("assetSchema type array at %s must not be empty", path)
		}
		seen := map[string]bool{}
		for _, item := range t {
			s, ok := item.(string)
			if !ok || !allowedTypeValues[s] {
				return fmt.Errorf("assetSchema type array at %s contains an unrecognized value", path)
			}
			if seen[s] {
				return fmt.Errorf("assetSchema type array at %s contains duplicate %q", path, s)
			}
			seen[s] = true
		}
	default:
		return fmt.Errorf("assetSchema type at %s must be a string or array of strings", path)
	}
	return nil
}
