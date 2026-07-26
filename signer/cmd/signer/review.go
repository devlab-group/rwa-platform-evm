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

// normalizedAmount renders a raw smallest-unit token amount as a
// human-readable decimal string with decimals fractional digits. Showing
// only the raw integer amount made a wrong tokenDecimals -- or an auditor
// misreading the integer's magnitude -- easy to miss, so we display both.
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

// resolvePointer resolves an RFC 6901 JSON pointer (e.g. "/serialNumber")
// against a parsed canonical.Value document. It is intentionally lenient:
// an unresolvable pointer renders as "(not present)" on the review screen
// rather than aborting the review, since displayFields is documentation
// only and must never block signing on its own.
func resolvePointer(root canonical.Value, pointer string) (canonical.Value, bool) {
	if pointer == "" || pointer == "/" {
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
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(t) {
				return nil, false
			}
			cur = t[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// displayString renders a canonical.Value for the human review screen.
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

// buildDisplayFields resolves a profile's displayFields against the parsed
// asset object, for the offline review screen only -- never used for hashing
// or validation.
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

// proofFields renders one review-screen line per metadata proof, clearly
// warning when no attached file's content hashes to the recorded value --
// the auditor needs to know when only a hash is present and the proof file
// itself is missing.
func proofFields(meta *metadata.Metadata, files map[string][]byte) []ui.Field {
	if len(meta.Proofs) == 0 {
		return nil
	}
	// Index attached files by content hash so a proof can be matched
	// regardless of its filename inside proofs/.
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
				Value: fmt.Sprintf("sha256:%s -- NO ATTACHED FILE MATCHES THIS HASH", p.SHA256),
				Warn:  true,
			})
		}
	}
	return fields
}
