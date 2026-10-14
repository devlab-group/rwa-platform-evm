package assets

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file enforces the assetSchema dialect "rwa-profile-assetSchema/1" defined
// in docs/spec/profile-schema-dialect.md.
//
// The server and the offline signer must agree on which schema keywords apply.
// The server compiles with a full Draft 2020-12 validator while the signer uses
// its own, so a schema with a keyword such as multipleOf could be enforced by one
// and ignored by the other. Both therefore accept only the same allowlist of
// keywords and reject every other one. The vectors in
// shared/vectors/schema-dialect-conformance.json check that parity, and
// TestDialectConformance runs them.

// allowedValidationKeywords are the dialect's validation and applicator keywords
var allowedValidationKeywords = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true,
	"items": true, "$ref": true,
	"enum": true, "const": true,
	"minLength": true, "maxLength": true, "pattern": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"minItems": true, "maxItems": true, "uniqueItems": true,
}

// allowedAnnotationKeywords are accepted and have no validation effect
var allowedAnnotationKeywords = map[string]bool{
	"title": true, "description": true, "default": true, "examples": true,
	"$comment": true, "$defs": true, "$id": true, "$schema": true,
	"deprecated": true, "readOnly": true, "writeOnly": true,
}

// opaqueDataKeywords hold data, not schemas, so their values are not walked
// (a const of {"oneOf": 1} does not use the oneOf keyword)
var opaqueDataKeywords = map[string]bool{
	"enum": true, "const": true, "default": true, "examples": true,
}

// allowedTypeValues are the primitive type names the dialect accepts in "type"
var allowedTypeValues = map[string]bool{
	"object": true, "array": true, "string": true, "integer": true,
	"number": true, "boolean": true, "null": true,
}

// maxRefDepth bounds the length of a $ref chain
const maxRefDepth = 32

// guardAssetSchema rejects a raw assetSchema that falls outside the
// rwa-profile-assetSchema/1 dialect, before it is compiled. It checks the keys
// of every schema object, the "type" values, and every $ref: it must be local,
// the only member of its object, resolvable, and not part of a cycle.
func guardAssetSchema(raw json.RawMessage) error {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("assetSchema is not valid JSON: %w", err)
	}
	g := &schemaGuard{root: root}
	return g.walkSchema(root, "$", nil)
}

// schemaGuard holds the parsed document root, against which $ref pointers resolve
type schemaGuard struct{ root any }

// walkSchema checks node as a schema object at path. refChain holds the $ref
// pointers already followed on the current path and is nil at the start of an
// independent walk, such as each $defs entry
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
		// every $defs entry must satisfy the dialect even when nothing refers to it
		for name, sub := range defs {
			if err := g.walkSchema(sub, path+".$defs."+name, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkRef resolves ref against g.root and checks the target. It rejects a direct
// or indirect cycle and a chain longer than maxRefDepth.
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
