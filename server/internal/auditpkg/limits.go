package auditpkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// safeIntegerBound is the cross-language safe integer range boundary,
// 2^53-1, per docs/spec/asset-profile.md ("JSON integers that DO appear
// MUST stay within [-(2^53-1), 2^53-1] so Go/TS/JSON agree").
var safeIntegerBound = new(big.Int).SetInt64(9007199254740991) // 2^53 - 1

// container tracks one open JSON object or array while walking tokens, so
// object key-value pairs and array elements can be counted (and bounded)
// separately per docs/spec/asset-profile.md's distinct
// "Max array length" / "Max object keys" limits.
type container struct {
	isObject      bool
	count         int
	awaitingValue bool // for objects: true once a key has been read and its value is pending
}

// limitDecoder walks JSON tokens to enforce platform-wide depth/length
// bounds independent of any JSON Schema the caller may also apply.
type limitDecoder struct {
	dec   *json.Decoder
	depth int
	stack []container
}

func newLimitDecoder(raw []byte) *limitDecoder {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return &limitDecoder{dec: dec}
}

func (d *limitDecoder) walk() error {
	for {
		tok, err := d.dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return nil
			}
			return fmt.Errorf("auditpkg: invalid JSON: %w", err)
		}
		if err := d.handle(tok); err != nil {
			return err
		}
	}
}

func (d *limitDecoder) handle(tok json.Token) error {
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{', '[':
			d.depth++
			if d.depth > MaxObjectDepth {
				return fmt.Errorf("auditpkg: document exceeds max nesting depth %d", MaxObjectDepth)
			}
			d.stack = append(d.stack, container{isObject: v == '{'})
		case '}', ']':
			if d.depth > 0 {
				d.depth--
				d.stack = d.stack[:len(d.stack)-1]
			}
			return d.recordElement(false)
		}
		return nil
	case string:
		if len(v) > MaxStringLength {
			return fmt.Errorf("auditpkg: string value exceeds max length %d", MaxStringLength)
		}
		return d.recordElement(true)
	case json.Number:
		if err := checkSafeInteger(v); err != nil {
			return err
		}
		return d.recordElement(true)
	default:
		// bools, null
		return d.recordElement(true)
	}
}

// recordElement accounts for tok having just been consumed as either an
// object key/value or an array element, and enforces the matching limit.
// isLeaf distinguishes a scalar token from a just-closed nested container
// (both count as one "element" of their parent, but only a leaf can be an
// object key).
func (d *limitDecoder) recordElement(isLeaf bool) error {
	if len(d.stack) == 0 {
		return nil // top-level scalar/closed document; nothing to bound
	}
	top := &d.stack[len(d.stack)-1]
	if top.isObject {
		if isLeaf && !top.awaitingValue {
			// This token is a key; the pair completes when its value arrives.
			top.awaitingValue = true
			return nil
		}
		top.awaitingValue = false
	}
	top.count++
	if top.isObject {
		if top.count > MaxObjectKeys {
			return fmt.Errorf("auditpkg: object exceeds max key count %d", MaxObjectKeys)
		}
	} else if top.count > MaxArrayLength {
		return fmt.Errorf("auditpkg: array exceeds max length %d", MaxArrayLength)
	}
	return nil
}

// checkSafeInteger enforces docs/spec/asset-profile.md's cross-language
// safe integer range on any JSON number literal that has no fractional or
// exponent part (i.e. is actually an integer, not a float the range doesn't
// govern).
func checkSafeInteger(n json.Number) error {
	s := string(n)
	if strings.ContainsAny(s, ".eE") {
		return nil // not an integer literal
	}
	i, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return fmt.Errorf("auditpkg: invalid number literal %q", s)
	}
	if i.CmpAbs(safeIntegerBound) > 0 {
		return fmt.Errorf("auditpkg: integer %s exceeds the safe range [-(2^53-1), 2^53-1]; use a string instead", s)
	}
	return nil
}
