package auditpkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// safeIntegerBound is 2^53-1, the edge of the cross-language safe integer range in
// docs/spec/asset-profile.md ("Numeric rules")
var safeIntegerBound = new(big.Int).SetInt64(9007199254740991)

// container tracks one open JSON object or array during the token walk, so
// object pairs and array elements are counted against the separate "Max object
// keys" and "Max array length" limits in docs/spec/asset-profile.md
type container struct {
	isObject      bool
	count         int
	awaitingValue bool // for objects: true once a key has been read and its value is pending
}

// limitDecoder walks JSON tokens and enforces the structural limits
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
		// booleans and null
		return d.recordElement(true)
	}
}

// recordElement counts the token just consumed against its parent container and
// enforces the matching limit. A scalar or a just-closed nested container is one
// element of its parent. Only a scalar (isLeaf) can be an object key.
func (d *limitDecoder) recordElement(isLeaf bool) error {
	if len(d.stack) == 0 {
		return nil // outside any container, nothing to bound
	}
	top := &d.stack[len(d.stack)-1]
	if top.isObject {
		if isLeaf && !top.awaitingValue {
			// a key; the pair completes when its value arrives
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

// checkSafeInteger rejects an integer literal outside the cross-language safe
// range in docs/spec/asset-profile.md ("Numeric rules"). A literal with a
// fraction or exponent is not checked.
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
