// Package policy loads the auditor's independently-provisioned deployment
// trust root: a small local JSON file, maintained by the auditor
// themselves and never taken from the .rwa package under review, that
// pins the chain, SupplyController, Vault, auditor address, project, and
// profile digest; the signer refuses to sign anything that doesn't match.
//
// The alternative we rejected was a set of optional --expect-* flags:
// omitting all of them would let the signer sign using only values the
// package itself supplied, collapsing the independent air-gapped
// verification boundary into a self-consistency check the package producer
// fully controls. A policy file is mandatory for every sign invocation.
package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"
)

// DefaultMaxAttestationLifetime is the maximum allowed gap between
// metadata.createdAt and typed-data validUntil when a policy file does not
// override it via maxAttestationLifetimeHours. Roughly 30 days.
const DefaultMaxAttestationLifetime = 30 * 24 * time.Hour

// Policy is the auditor's trust root, loaded from a local file.
type Policy struct {
	ChainID       string
	Controller    string // SupplyController address (typed-data domain.verifyingContract)
	Vault         string
	Auditor       string
	ProjectID     string
	ProfileDigest string
	// MaxAttestationLifetime bounds how far typed-data validUntil may sit
	// after metadata.createdAt. Defaults to DefaultMaxAttestationLifetime.
	MaxAttestationLifetime time.Duration
}

// fileShape is the on-disk JSON shape of a policy file.
type fileShape struct {
	ChainID                     string  `json:"chainId"`
	Controller                  string  `json:"controller"`
	Vault                       string  `json:"vault"`
	Auditor                     string  `json:"auditor"`
	ProjectID                   string  `json:"projectId"`
	ProfileDigest               string  `json:"profileDigest"`
	MaxAttestationLifetimeHours float64 `json:"maxAttestationLifetimeHours,omitempty"`
}

// Load reads and validates a policy file from path. Every field except
// MaxAttestationLifetimeHours is required and must be non-empty -- there is
// no default trust root; a missing, malformed, or incomplete policy file
// is a hard failure, by design.
func Load(path string) (*Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: reading %s: %w", path, err)
	}

	var f fileShape
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("policy: parsing %s: %w", path, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("policy: %s: trailing data after JSON value", path)
	}

	required := []struct{ name, value string }{
		{"chainId", f.ChainID},
		{"controller", f.Controller},
		{"vault", f.Vault},
		{"auditor", f.Auditor},
		{"projectId", f.ProjectID},
		{"profileDigest", f.ProfileDigest},
	}
	for _, r := range required {
		if strings.TrimSpace(r.value) == "" {
			return nil, fmt.Errorf("policy: %s: field %q is required and must be non-empty", path, r.name)
		}
	}
	if _, ok := new(big.Int).SetString(f.ChainID, 10); !ok {
		return nil, fmt.Errorf("policy: %s: chainId must be a decimal integer, got %q", path, f.ChainID)
	}
	if f.MaxAttestationLifetimeHours < 0 {
		return nil, fmt.Errorf("policy: %s: maxAttestationLifetimeHours must not be negative", path)
	}

	lifetime := DefaultMaxAttestationLifetime
	if f.MaxAttestationLifetimeHours > 0 {
		lifetime = time.Duration(f.MaxAttestationLifetimeHours * float64(time.Hour))
	}

	return &Policy{
		ChainID:                f.ChainID,
		Controller:             f.Controller,
		Vault:                  f.Vault,
		Auditor:                f.Auditor,
		ProjectID:              f.ProjectID,
		ProfileDigest:          f.ProfileDigest,
		MaxAttestationLifetime: lifetime,
	}, nil
}
