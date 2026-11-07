// Package jsonschema implements "rwa-profile-assetSchema/1"
// (docs/spec/profile-schema-dialect.md). allowedKeywords lists permitted schema-node
// keys; other keys fail compilation to avoid silently ignored constraints.
//
// Values in enum, const, default and examples are opaque data. Local $ref nodes permit
// no siblings, including annotations, because this dialect omits Draft 2020-12 sibling
// semantics.
package jsonschema

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rwa-platform/signer/internal/canonical"
)

// allowedKeywords lists the dialect's permitted schema-node keys; opaque data values
// are outside this allowlist
var allowedKeywords = map[string]bool{
	// Annotations and definition containers; no assertions of their own
	"$schema":     true,
	"$id":         true,
	"$comment":    true,
	"$defs":       true,
	"title":       true,
	"description": true,
	"default":     true,
	"examples":    true,
	"deprecated":  true,
	"readOnly":    true,
	"writeOnly":   true,
	// Reference
	"$ref": true,
	// Implemented applicators and assertions
	"type":                 true,
	"properties":           true,
	"required":             true,
	"additionalProperties": true,
	"items":                true,
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

// maxSchemaDepth bounds shared recursion through nested schemas and $ref chains to
// protect the stack
const maxSchemaDepth = 100

// maxSchemaNodes caps nodes processed per Compile, limiting wide schemas and repeated
// acyclic $ref paths independently of depth
const maxSchemaNodes = 10000

// Schema is a compiled node of the supported dialect
type Schema struct {
	types                []string // empty means "any type"
	properties           map[string]*Schema
	required             map[string]bool
	additionalProperties *Schema // nil means "additionalProperties: true" (default)
	additionalPropsFalse bool
	items                *Schema
	enumKeys             map[string]bool // valueKey of each member; nil means no enum
	hasConst             bool
	constKey             string
	minLength            *int
	maxLength            *int
	pattern              *regexp.Regexp
	minimum              *big.Rat
	maximum              *big.Rat
	exclusiveMinimum     *big.Rat
	exclusiveMaximum     *big.Rat
	minItems             *int
	maxItems             *int
	uniqueItems          bool
}

// Compile builds a Schema from parsed canonical.Value input, rejecting unsupported
// schema-node keywords; CompileBytes handles JSON parsing checks
func Compile(root canonical.Value) (*Schema, error) {
	c := &compiler{root: root, visiting: make(map[string]bool)}
	return c.compile(root, "#", 0)
}

// CompileBytes parses and compiles a schema document from raw JSON bytes
func CompileBytes(raw []byte) (*Schema, error) {
	v, err := canonical.ParseWithLimits(raw, canonical.DefaultLimits())
	if err != nil {
		return nil, fmt.Errorf("jsonschema: %w", err)
	}
	return Compile(v)
}

type compiler struct {
	root canonical.Value
	// visiting tracks $ref strings on the current path; revisiting one rejects direct or
	// indirect cycles
	visiting map[string]bool
	// nodes counts processed schema nodes across this Compile, independently of depth
	nodes int
}

func (c *compiler) compile(v canonical.Value, path string, depth int) (*Schema, error) {
	if depth > maxSchemaDepth {
		return nil, fmt.Errorf("jsonschema: %s: schema exceeds the maximum nesting/$ref-chain depth of %d", path, maxSchemaDepth)
	}
	c.nodes++
	if c.nodes > maxSchemaNodes {
		return nil, fmt.Errorf("jsonschema: %s: schema exceeds the maximum node count of %d", path, maxSchemaNodes)
	}

	obj, ok := v.(*canonical.Object)
	if !ok {
		return nil, fmt.Errorf("jsonschema: %s: schema must be a JSON object", path)
	}

	for _, k := range obj.Keys() {
		if !allowedKeywords[k] {
			return nil, fmt.Errorf("jsonschema: %s: keyword %q is not permitted by the V1 schema subset", path, k)
		}
	}

	if refVal, present := obj.Get("$ref"); present {
		if obj.Len() != 1 {
			return nil, fmt.Errorf("jsonschema: %s: $ref must not have sibling keywords (Draft 2020-12 $ref-sibling semantics is not implemented; use $ref alone)", path)
		}
		ref, ok := refVal.(string)
		if !ok {
			return nil, fmt.Errorf("jsonschema: %s: $ref must be a string", path)
		}
		if !strings.HasPrefix(ref, "#/") && ref != "#" {
			return nil, fmt.Errorf("jsonschema: %s: only local \"#/...\" $ref is permitted, got %q", path, ref)
		}
		// The server's compiler percent-decodes the fragment, so a pointer here is literal
		// and may not contain '%'
		if strings.Contains(ref, "%") {
			return nil, fmt.Errorf("jsonschema: %s: $ref must not contain '%%', got %q", path, ref)
		}
		if c.visiting[ref] {
			return nil, fmt.Errorf("jsonschema: %s: cyclic $ref detected at %q", path, ref)
		}
		target, err := resolvePointer(c.root, ref)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: %s: %w", path, err)
		}
		c.visiting[ref] = true
		sub, err := c.compile(target, path+"->"+ref, depth+1)
		delete(c.visiting, ref)
		return sub, err
	}

	if err := checkAnnotationTypes(obj, path); err != nil {
		return nil, err
	}

	s := &Schema{}

	if tv, present := obj.Get("type"); present {
		switch t := tv.(type) {
		case string:
			s.types = []string{t}
		case []canonical.Value:
			// Reject empty type arrays, as the server does; otherwise nil s.types would
			// incorrectly allow any type
			if len(t) == 0 {
				return nil, fmt.Errorf("jsonschema: %s.type: type array must not be empty", path)
			}
			for _, e := range t {
				str, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("jsonschema: %s: type array must contain only strings", path)
				}
				s.types = append(s.types, str)
			}
		default:
			return nil, fmt.Errorf("jsonschema: %s: type must be a string or array of strings", path)
		}
		seenTypes := make(map[string]bool, len(s.types))
		for _, t := range s.types {
			switch t {
			case "object", "array", "string", "integer", "number", "boolean", "null":
			default:
				return nil, fmt.Errorf("jsonschema: %s: unsupported type %q", path, t)
			}
			if seenTypes[t] {
				return nil, fmt.Errorf("jsonschema: %s.type: duplicate type %q", path, t)
			}
			seenTypes[t] = true
		}
	}

	// Check unused $defs too for unsupported keywords/cycles. Discard compiled results:
	// definitions supply $ref targets, not instance-validation branches.
	if defsVal, present := obj.Get("$defs"); present {
		defsObj, ok := defsVal.(*canonical.Object)
		if !ok {
			return nil, fmt.Errorf("jsonschema: %s.$defs: must be an object", path)
		}
		for _, k := range defsObj.Keys() {
			dv, _ := defsObj.Get(k)
			if _, err := c.compile(dv, path+".$defs."+k, depth+1); err != nil {
				return nil, err
			}
		}
	}

	if pv, present := obj.Get("properties"); present {
		pobj, ok := pv.(*canonical.Object)
		if !ok {
			return nil, fmt.Errorf("jsonschema: %s.properties: must be an object", path)
		}
		s.properties = make(map[string]*Schema, pobj.Len())
		for _, k := range pobj.Keys() {
			pv, _ := pobj.Get(k)
			sub, err := c.compile(pv, path+".properties."+k, depth+1)
			if err != nil {
				return nil, err
			}
			s.properties[k] = sub
		}
	}

	if rv, present := obj.Get("required"); present {
		arr, ok := rv.([]canonical.Value)
		if !ok {
			return nil, fmt.Errorf("jsonschema: %s.required: must be an array", path)
		}
		s.required = make(map[string]bool, len(arr))
		for _, e := range arr {
			str, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("jsonschema: %s.required: must contain only strings", path)
			}
			if s.required[str] {
				return nil, fmt.Errorf("jsonschema: %s.required: duplicate name %q", path, str)
			}
			s.required[str] = true
		}
	}

	if av, present := obj.Get("additionalProperties"); present {
		switch t := av.(type) {
		case bool:
			s.additionalPropsFalse = !t
		case *canonical.Object:
			sub, err := c.compile(t, path+".additionalProperties", depth+1)
			if err != nil {
				return nil, err
			}
			s.additionalProperties = sub
		default:
			return nil, fmt.Errorf("jsonschema: %s.additionalProperties: must be a boolean or schema object", path)
		}
	}

	if iv, present := obj.Get("items"); present {
		sub, err := c.compile(iv, path+".items", depth+1)
		if err != nil {
			return nil, err
		}
		s.items = sub
	}

	if ev, present := obj.Get("enum"); present {
		arr, ok := ev.([]canonical.Value)
		if !ok {
			return nil, fmt.Errorf("jsonschema: %s.enum: must be an array", path)
		}
		if len(arr) == 0 {
			return nil, fmt.Errorf("jsonschema: %s.enum: must not be empty", path)
		}
		s.enumKeys = make(map[string]bool, len(arr))
		for _, e := range arr {
			k, err := valueKey(e, schemaRat)
			if err != nil {
				return nil, fmt.Errorf("jsonschema: %s.enum: %w", path, err)
			}
			s.enumKeys[k] = true
		}
	}

	if cv, present := obj.Get("const"); present {
		k, err := valueKey(cv, schemaRat)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: %s.const: %w", path, err)
		}
		s.hasConst = true
		s.constKey = k
	}

	if v, present := obj.Get("minLength"); present {
		n, err := requireNonNegInt(v, path, "minLength")
		if err != nil {
			return nil, err
		}
		s.minLength = &n
	}
	if v, present := obj.Get("maxLength"); present {
		n, err := requireNonNegInt(v, path, "maxLength")
		if err != nil {
			return nil, err
		}
		s.maxLength = &n
	}
	if v, present := obj.Get("pattern"); present {
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("jsonschema: %s.pattern: must be a string", path)
		}
		re, err := regexp.Compile(str)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: %s.pattern: invalid regexp: %w", path, err)
		}
		s.pattern = re
	}

	if v, present := obj.Get("minimum"); present {
		f, err := requireNumber(v, path, "minimum")
		if err != nil {
			return nil, err
		}
		s.minimum = f
	}
	if v, present := obj.Get("maximum"); present {
		f, err := requireNumber(v, path, "maximum")
		if err != nil {
			return nil, err
		}
		s.maximum = f
	}
	if v, present := obj.Get("exclusiveMinimum"); present {
		f, err := requireNumber(v, path, "exclusiveMinimum")
		if err != nil {
			return nil, err
		}
		s.exclusiveMinimum = f
	}
	if v, present := obj.Get("exclusiveMaximum"); present {
		f, err := requireNumber(v, path, "exclusiveMaximum")
		if err != nil {
			return nil, err
		}
		s.exclusiveMaximum = f
	}

	if v, present := obj.Get("minItems"); present {
		n, err := requireNonNegInt(v, path, "minItems")
		if err != nil {
			return nil, err
		}
		s.minItems = &n
	}
	if v, present := obj.Get("maxItems"); present {
		n, err := requireNonNegInt(v, path, "maxItems")
		if err != nil {
			return nil, err
		}
		s.maxItems = &n
	}
	if v, present := obj.Get("uniqueItems"); present {
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("jsonschema: %s.uniqueItems: must be a boolean", path)
		}
		s.uniqueItems = b
	}

	// Annotations have no validation effect

	return s, nil
}

func requireNonNegInt(v canonical.Value, path, kw string) (int, error) {
	num, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("jsonschema: %s.%s: must be an integer", path, kw)
	}
	n, err := strconv.Atoi(num.String())
	if err != nil || n < 0 {
		return 0, fmt.Errorf("jsonschema: %s.%s: must be a non-negative integer", path, kw)
	}
	return n, nil
}

func requireNumber(v canonical.Value, path, kw string) (*big.Rat, error) {
	num, ok := v.(json.Number)
	if !ok {
		return nil, fmt.Errorf("jsonschema: %s.%s: must be a number", path, kw)
	}
	r, err := schemaRat(num)
	if err != nil {
		return nil, fmt.Errorf("jsonschema: %s.%s: %w", path, kw, err)
	}
	return r, nil
}

// checkAnnotationTypes applies the value types of the Draft 2020-12 meta-data and core
// vocabularies to annotations at a schema position, as the server's compiler does
func checkAnnotationTypes(obj *canonical.Object, path string) error {
	for _, kw := range []string{"title", "description", "$comment"} {
		if v, present := obj.Get(kw); present {
			if _, ok := v.(string); !ok {
				return fmt.Errorf("jsonschema: %s.%s: must be a string", path, kw)
			}
		}
	}
	for _, kw := range []string{"deprecated", "readOnly", "writeOnly"} {
		if v, present := obj.Get(kw); present {
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("jsonschema: %s.%s: must be a boolean", path, kw)
			}
		}
	}
	if v, present := obj.Get("examples"); present {
		if _, ok := v.([]canonical.Value); !ok {
			return fmt.Errorf("jsonschema: %s.examples: must be an array", path)
		}
	}
	return nil
}

// schemaRat returns the exact value of a number written in the schema, such as a bound
// or an enum member. A literal outside the float64 range is refused, as the server
// refuses it when it decodes the schema.
func schemaRat(n json.Number) (*big.Rat, error) {
	if _, err := strconv.ParseFloat(string(n), 64); err != nil {
		return nil, fmt.Errorf("number %q is outside the float64 range", string(n))
	}
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return nil, fmt.Errorf("invalid number %q", string(n))
	}
	return r, nil
}

// instanceRat rounds an instance number to float64 and returns its printed decimal value.
// Canonicalization and server validation use that rounded value too, keeping integer
// typing, bounds, enum, const and uniqueItems consistent.
func instanceRat(n json.Number) (*big.Rat, error) {
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil {
		return nil, fmt.Errorf("number %q is outside the float64 range", string(n))
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
	if !ok {
		return nil, fmt.Errorf("invalid number %q", string(n))
	}
	return r, nil
}

// valueKey serializes v so two values have the same key exactly when the dialect treats
// them as equal: numbers by exact rational value, objects by member set. rat supplies a
// number's value, schemaRat for schema literals and instanceRat for instance data.
func valueKey(v canonical.Value, rat func(json.Number) (*big.Rat, error)) (string, error) {
	var b strings.Builder
	if err := writeKey(&b, v, rat); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeKey(b *strings.Builder, v canonical.Value, rat func(json.Number) (*big.Rat, error)) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("z")
	case bool:
		b.WriteString(strconv.FormatBool(t)[:1])
	case json.Number:
		r, err := rat(t)
		if err != nil {
			return err
		}
		b.WriteString("n" + r.RatString() + ";")
	case string:
		fmt.Fprintf(b, "s%d:%s", len(t), t)
	case []canonical.Value:
		b.WriteString("[")
		for _, e := range t {
			if err := writeKey(b, e, rat); err != nil {
				return err
			}
		}
		b.WriteString("]")
	case *canonical.Object:
		keys := append([]string(nil), t.Keys()...)
		sort.Strings(keys)
		b.WriteString("{")
		for _, k := range keys {
			fmt.Fprintf(b, "%d:%s", len(k), k)
			e, _ := t.Get(k)
			if err := writeKey(b, e, rat); err != nil {
				return err
			}
		}
		b.WriteString("}")
	default:
		return fmt.Errorf("unsupported value type %T", v)
	}
	return nil
}

// resolvePointer resolves a local JSON pointer such as "#/a/b" against root
func resolvePointer(root canonical.Value, ref string) (canonical.Value, error) {
	if ref == "#" {
		return root, nil
	}
	frag := strings.TrimPrefix(ref, "#/")
	cur := root
	for _, tok := range strings.Split(frag, "/") {
		tok = strings.ReplaceAll(tok, "~1", "/")
		tok = strings.ReplaceAll(tok, "~0", "~")
		// Traverse objects, not arrays; numeric tokens address object members only
		obj, ok := cur.(*canonical.Object)
		if !ok {
			return nil, fmt.Errorf("$ref pointer segment %q does not resolve within the document", tok)
		}
		v, ok := obj.Get(tok)
		if !ok {
			return nil, fmt.Errorf("$ref pointer segment %q not found", tok)
		}
		cur = v
	}
	return cur, nil
}

// ValidateBytes parses raw JSON data and validates it against s
func (s *Schema) ValidateBytes(data []byte) error {
	v, err := canonical.ParseWithLimits(data, canonical.DefaultLimits())
	if err != nil {
		return fmt.Errorf("jsonschema: %w", err)
	}
	return s.Validate(v)
}

// Validate validates an already-parsed canonical.Value against s
func (s *Schema) Validate(v canonical.Value) error {
	return s.validateAt(v, "$")
}

// typeOf names v's JSON type; num is v's value when v is a number
func typeOf(v canonical.Value, num *big.Rat) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number:
		if num.IsInt() {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case []canonical.Value:
		return "array"
	case *canonical.Object:
		return "object"
	default:
		return "unknown"
	}
}

func (s *Schema) validateAt(v canonical.Value, path string) error {
	var num *big.Rat
	if n, ok := v.(json.Number); ok {
		r, err := instanceRat(n)
		if err != nil {
			return fmt.Errorf("jsonschema: %s: %w", path, err)
		}
		num = r
	}
	if len(s.types) > 0 {
		actual := typeOf(v, num)
		matched := false
		for _, t := range s.types {
			if t == actual || (t == "number" && actual == "integer") {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("jsonschema: %s: type mismatch: got %s, want %s", path, actual, strings.Join(s.types, "|"))
		}
	}

	if s.hasConst || s.enumKeys != nil {
		key, err := valueKey(v, instanceRat)
		if err != nil {
			return fmt.Errorf("jsonschema: %s: %w", path, err)
		}
		if s.hasConst && key != s.constKey {
			return fmt.Errorf("jsonschema: %s: value does not match const", path)
		}
		if s.enumKeys != nil && !s.enumKeys[key] {
			return fmt.Errorf("jsonschema: %s: value not in enum", path)
		}
	}

	switch t := v.(type) {
	case string:
		if err := s.validateString(t, path); err != nil {
			return err
		}
	case json.Number:
		if err := s.validateNumber(t, num, path); err != nil {
			return err
		}
	case []canonical.Value:
		if err := s.validateArray(t, path); err != nil {
			return err
		}
	case *canonical.Object:
		if err := s.validateObject(t, path); err != nil {
			return err
		}
	}

	return nil
}

func (s *Schema) validateString(str string, path string) error {
	n := utf8.RuneCountInString(str)
	if s.minLength != nil && n < *s.minLength {
		return fmt.Errorf("jsonschema: %s: length %d is less than minLength %d", path, n, *s.minLength)
	}
	if s.maxLength != nil && n > *s.maxLength {
		return fmt.Errorf("jsonschema: %s: length %d exceeds maxLength %d", path, n, *s.maxLength)
	}
	if s.pattern != nil && !s.pattern.MatchString(str) {
		return fmt.Errorf("jsonschema: %s: does not match pattern %q", path, s.pattern.String())
	}
	return nil
}

func (s *Schema) validateNumber(lit json.Number, f *big.Rat, path string) error {
	if s.minimum != nil && f.Cmp(s.minimum) < 0 {
		return fmt.Errorf("jsonschema: %s: %s is less than minimum %s", path, lit.String(), s.minimum.RatString())
	}
	if s.maximum != nil && f.Cmp(s.maximum) > 0 {
		return fmt.Errorf("jsonschema: %s: %s exceeds maximum %s", path, lit.String(), s.maximum.RatString())
	}
	if s.exclusiveMinimum != nil && f.Cmp(s.exclusiveMinimum) <= 0 {
		return fmt.Errorf("jsonschema: %s: %s must be greater than exclusiveMinimum %s", path, lit.String(), s.exclusiveMinimum.RatString())
	}
	if s.exclusiveMaximum != nil && f.Cmp(s.exclusiveMaximum) >= 0 {
		return fmt.Errorf("jsonschema: %s: %s must be less than exclusiveMaximum %s", path, lit.String(), s.exclusiveMaximum.RatString())
	}
	return nil
}

func (s *Schema) validateArray(arr []canonical.Value, path string) error {
	if s.minItems != nil && len(arr) < *s.minItems {
		return fmt.Errorf("jsonschema: %s: length %d is less than minItems %d", path, len(arr), *s.minItems)
	}
	if s.maxItems != nil && len(arr) > *s.maxItems {
		return fmt.Errorf("jsonschema: %s: length %d exceeds maxItems %d", path, len(arr), *s.maxItems)
	}
	if s.uniqueItems {
		seen := make(map[string]bool, len(arr))
		for _, e := range arr {
			key, err := valueKey(e, instanceRat)
			if err != nil {
				return fmt.Errorf("jsonschema: %s: %w", path, err)
			}
			if seen[key] {
				return fmt.Errorf("jsonschema: %s: array items must be unique", path)
			}
			seen[key] = true
		}
	}
	if s.items != nil {
		for i, e := range arr {
			if err := s.items.validateAt(e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Schema) validateObject(obj *canonical.Object, path string) error {
	for req := range s.required {
		if _, ok := obj.Get(req); !ok {
			return fmt.Errorf("jsonschema: %s: missing required property %q", path, req)
		}
	}
	for _, k := range obj.Keys() {
		v, _ := obj.Get(k)
		if sub, ok := s.properties[k]; ok {
			if err := sub.validateAt(v, path+"."+k); err != nil {
				return err
			}
			continue
		}
		if s.additionalPropsFalse {
			return fmt.Errorf("jsonschema: %s: additional property %q is not allowed", path, k)
		}
		if s.additionalProperties != nil {
			if err := s.additionalProperties.validateAt(v, path+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}
