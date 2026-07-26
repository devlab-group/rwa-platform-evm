package assets

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file enforces the frozen operator assetSchema dialect
// "rwa-profile-assetSchema/1".
//
// The server and the offline signer must agree on exactly which schema
// keywords are meaningful. A blacklist approach — reject a few keywords,
// silently accept the rest — invites drift: a schema using e.g.
// multipleOf/format/patternProperties could be enforced by the server's full
// Draft 2020-12 compiler yet silently ignored by the signer's hand-written
// validator, letting a manually built .rwa package carry metadata the server
// would reject but the signer happily signs. Both sides therefore use the
// same closed allowlist: anything not explicitly supported is rejected, not
// ignored. Differential parity against
// shared/vectors/schema-dialect-conformance.json is a release gate — see
// TestSchemaDialectConformanceVectors.

// allowedValidationKeywords are the dialect's supported validation/applicator
// keywords (enforced identically by the server compiler and the signer).
var allowedValidationKeywords = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true,
	"items": true, "$ref": true,
	"enum": true, "const": true,
	"minLength": true, "maxLength": true, "pattern": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"minItems": true, "maxItems": true, "uniqueItems": true,
}

// allowedAnnotationKeywords are permitted but not enforced for validation.
var allowedAnnotationKeywords = map[string]bool{
	"title": true, "description": true, "default": true, "examples": true,
	"$comment": true, "$defs": true, "$id": true, "$schema": true,
	"deprecated": true, "readOnly": true, "writeOnly": true,
}

// opaqueDataKeywords hold data, not nested schemas: their values are never
// walked as schema objects (a `const` value of {"oneOf": 1} is data, not a
// use of the "oneOf" keyword).
var opaqueDataKeywords = map[string]bool{
	"enum": true, "const": true, "default": true, "examples": true,
}

// allowedTypeValues are the JSON Schema primitive type names the dialect
// recognizes for the "type" keyword (string or array-of-strings).
var allowedTypeValues = map[string]bool{
	"object": true, "array": true, "string": true, "integer": true,
	"number": true, "boolean": true, "null": true,
}

// maxRefDepth bounds $ref resolution chains so a cyclical or merely very
// long reference graph fails deterministically instead of recursing
// unboundedly — without this bound a self-referential schema would recurse
// through compile until the process crashes.
const maxRefDepth = 32

// guardAssetSchema walks a raw operator-supplied JSON Schema document and
// rejects anything outside the frozen rwa-profile-assetSchema/1 allowlist —
// every schema object's keys, "type" values, and every $ref (locality,
// sole-member rule, resolvability, cycle-freedom) — before compilation, so
// an unsupported/malicious schema is rejected with a clear reason instead
// of silently behaving differently across the Go validator and the offline
// signer.
func guardAssetSchema(raw json.RawMessage) error {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("assetSchema is not valid JSON: %w", err)
	}
	g := &schemaGuard{root: root}
	return g.walkSchema(root, "$", nil)
}

// schemaGuard carries the parsed document root so $ref targets can be
// resolved by JSON pointer against it.
type schemaGuard struct{ root any }

// walkSchema validates node as a schema object at path. refChain is the set
// of $ref pointer strings already traversed on the current resolution path
// (nil at the start of every independent walk — e.g. each direct $defs
// entry gets its own fresh chain in addition to whatever chain reaches it
// via an actual $ref).
func (g *schemaGuard) walkSchema(node any, path string, refChain map[string]bool) error {
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
		return g.walkRef(ref, path, refChain)
	}

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
	if propsRaw, ok := obj["properties"]; ok {
		props, ok := propsRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("assetSchema properties at %s must be an object", path)
		}
		for name, sub := range props {
			if err := g.walkSchema(sub, path+".properties."+name, refChain); err != nil {
				return err
			}
		}
	}
	if apRaw, ok := obj["additionalProperties"]; ok {
		if _, isBool := apRaw.(bool); !isBool {
			if err := g.walkSchema(apRaw, path+".additionalProperties", refChain); err != nil {
				return err
			}
		}
	}
	if itemsRaw, ok := obj["items"]; ok {
		if err := g.walkSchema(itemsRaw, path+".items", refChain); err != nil {
			return err
		}
	}
	if defsRaw, ok := obj["$defs"]; ok {
		defs, ok := defsRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("assetSchema $defs at %s must be an object", path)
		}
		// Every $defs entry MUST itself satisfy the dialect regardless of
		// whether anything actually $refs it — each gets its own fresh
		// refChain since this is an independent walk, not a resolution hop.
		for name, sub := range defs {
			if err := g.walkSchema(sub, path+".$defs."+name, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkRef resolves ref against g.root and validates the target, rejecting a
// direct or indirect cycle and bounding the chain length.
func (g *schemaGuard) walkRef(ref, path string, refChain map[string]bool) error {
	if refChain[ref] {
		return fmt.Errorf("assetSchema $ref cycle detected at %s (%s)", path, ref)
	}
	if len(refChain) >= maxRefDepth {
		return fmt.Errorf("assetSchema $ref chain exceeds max depth %d at %s", maxRefDepth, path)
	}
	target, err := resolvePointer(g.root, ref)
	if err != nil {
		return fmt.Errorf("assetSchema $ref %q at %s: %w", ref, path, err)
	}
	chain := make(map[string]bool, len(refChain)+1)
	for k := range refChain {
		chain[k] = true
	}
	chain[ref] = true
	return g.walkSchema(target, path+"->"+ref, chain)
}

// resolvePointer resolves a local "#" / "#/a/b/c" JSON pointer against root,
// per RFC 6901 (~1 -> "/", ~0 -> "~").
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

// validateTypeValue checks a "type" keyword's value is a recognized
// primitive type name, or an array of them.
func validateTypeValue(v any, path string) error {
	switch t := v.(type) {
	case string:
		if !allowedTypeValues[t] {
			return fmt.Errorf("assetSchema type %q at %s is not a recognized JSON Schema type", t, path)
		}
	case []any:
		for _, item := range t {
			s, ok := item.(string)
			if !ok || !allowedTypeValues[s] {
				return fmt.Errorf("assetSchema type array at %s contains an unrecognized value", path)
			}
		}
	default:
		return fmt.Errorf("assetSchema type at %s must be a string or array of strings", path)
	}
	return nil
}
