package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/rwa-platform/signer/internal/canonical"
	"github.com/rwa-platform/signer/internal/metadata"
	"github.com/rwa-platform/signer/internal/profile"
	"github.com/rwa-platform/signer/internal/ui"
)

// normalizedAmount renders smallest units with decimals fractional digits beside the
// raw amount, helping reviewers spot wrong tokenDecimals or magnitudes
func normalizedAmount(amount *big.Int, decimals int) string {
	if decimals <= 0 {
		return amount.String()
	}
	s := amount.String()
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	for len(s) <= decimals {
		s = "0" + s
	}
	intPart := s[:len(s)-decimals]
	fracPart := s[len(s)-decimals:]
	out := intPart + "." + fracPart
	if neg {
		out = "-" + out
	}
	return out
}

// resolvePointer follows RFC 6901: "" selects the document, "/" the empty-name member,
// and array indices match 0|[1-9][0-9]*, excluding "-". Unresolved optional
// displayFields return not found without blocking signing.
func resolvePointer(root canonical.Value, pointer string) (canonical.Value, bool) {
	if pointer == "" {
		return root, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	cur := root
	for _, tok := range strings.Split(pointer[1:], "/") {
		tok = strings.ReplaceAll(tok, "~1", "/")
		tok = strings.ReplaceAll(tok, "~0", "~")
		switch t := cur.(type) {
		case *canonical.Object:
			v, ok := t.Get(tok)
			if !ok {
				return nil, false
			}
			cur = v
		case []canonical.Value:
			if !validIndex(tok) {
				return nil, false
			}
			idx, err := strconv.Atoi(tok)
			if err != nil || idx >= len(t) {
				return nil, false
			}
			cur = t[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// validIndex reports whether tok matches the RFC 6901 array index grammar 0|[1-9][0-9]*
func validIndex(tok string) bool {
	if tok == "" || (len(tok) > 1 && tok[0] == '0') {
		return false
	}
	for _, c := range tok {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// displayString renders a canonical.Value for the review screen
func displayString(v canonical.Value) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		b, err := canonical.Marshal(v)
		if err != nil {
			return fmt.Sprintf("<unrenderable: %v>", err)
		}
		return string(b)
	}
}

// buildDisplayFields resolves profile pointers against asset for review only, without
// affecting hashing/validation; missing pointers show "(not present)" with a warning
func buildDisplayFields(p *profile.Profile, asset canonical.Value) []ui.Field {
	fields := make([]ui.Field, 0, len(p.DisplayFields))
	for _, df := range p.DisplayFields {
		v, ok := resolvePointer(asset, df.Pointer)
		if !ok {
			fields = append(fields, ui.Field{Label: df.Label, Value: "(not present)", Warn: true})
			continue
		}
		fields = append(fields, ui.Field{Label: df.Label, Value: displayString(v)})
	}
	return fields
}

// legalFields displays profileDigest-bound legal members without fetching document
// references
func legalFields(l *profile.Legal) []ui.Field {
	if l == nil {
		return nil
	}
	fields := []ui.Field{{Label: "legal wrapper", Value: l.Wrapper}}
	if l.IssuerLEI != "" {
		fields = append(fields, ui.Field{Label: "legal issuerLei", Value: l.IssuerLEI})
	}
	if l.HolderRights != "" {
		fields = append(fields, ui.Field{Label: "legal holderRights", Value: l.HolderRights})
	}
	if r := l.RedemptionTerms; r != nil {
		fields = append(fields, ui.Field{Label: "legal redemptionTerms", Value: fmt.Sprintf("uri:%s sha256:%s", r.URI, r.SHA256)})
	}
	if l.MaturityDate != "" {
		fields = append(fields, ui.Field{Label: "legal maturityDate", Value: l.MaturityDate})
	}
	if r := l.Rating; r != nil {
		v := "provider:" + r.Provider
		if r.Grade != "" {
			v += " grade:" + r.Grade
		}
		fields = append(fields, ui.Field{Label: "legal rating", Value: fmt.Sprintf("%s uri:%s sha256:%s", v, r.Ref.URI, r.Ref.SHA256)})
	}
	return fields
}

// proofFields renders each metadata proof, warning when no attached file matches its
// hash
func proofFields(meta *metadata.Metadata, files map[string][]byte) []ui.Field {
	if len(meta.Proofs) == 0 {
		return nil
	}
	// Match proofs by content hash, independent of filenames in proofs/
	byHash := make(map[string]string, len(files))
	for name, data := range files {
		if !strings.HasPrefix(name, "proofs/") {
			continue
		}
		sum := sha256.Sum256(data)
		byHash[hex.EncodeToString(sum[:])] = name
	}

	fields := make([]ui.Field, 0, len(meta.Proofs))
	for _, p := range meta.Proofs {
		if name, ok := byHash[p.SHA256]; ok {
			fields = append(fields, ui.Field{
				Label: "proof: " + p.Type,
				Value: fmt.Sprintf("sha256:%s file:%s", p.SHA256, name),
			})
		} else {
			fields = append(fields, ui.Field{
				Label: "proof: " + p.Type,
				Value: fmt.Sprintf("sha256:%s, NO ATTACHED FILE MATCHES THIS HASH", p.SHA256),
				Warn:  true,
			})
		}
	}
	return fields
}
