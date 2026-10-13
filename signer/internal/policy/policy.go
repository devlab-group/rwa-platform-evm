// Package policy loads the auditor's local deployment policy, maintained
// independently of the package under review. Every signing run requires it
// to pin the chain, controller, Vault, auditor, project and profile digest.
package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// DefaultMaxAttestationLifetime is the 30-day expiry window used when
// maxAttestationLifetimeHours is omitted or zero
const DefaultMaxAttestationLifetime = 30 * 24 * time.Hour

// Policy holds the auditor's trusted deployment values from a local file
type Policy struct {
	ChainID       string
	Controller    string // SupplyController address (typed-data domain.verifyingContract)
	Vault         string
	Auditor       string
	ProjectID     string
	ProfileDigest string
	// MaxAttestationLifetime bounds validUntil relative to both signing time
	// and metadata.createdAt, defaulting to DefaultMaxAttestationLifetime
	MaxAttestationLifetime time.Duration
	// Safe is set when Auditor is a Safe multisig; nil for a single-key auditor
	Safe *Safe
}

// Safe holds the independently verified owner list and threshold for the auditor Safe
type Safe struct {
	Owners    []common.Address
	Threshold int
}

// IsOwner reports whether addr is listed in the policy
func (s *Safe) IsOwner(addr common.Address) bool {
	for _, owner := range s.Owners {
		if owner == addr {
			return true
		}
	}
	return false
}

// fileShape is the policy's on-disk JSON representation
type fileShape struct {
	ChainID                     string  `json:"chainId"`
	Controller                  string  `json:"controller"`
	Vault                       string  `json:"vault"`
	Auditor                     string  `json:"auditor"`
	ProjectID                   string  `json:"projectId"`
	ProfileDigest               string  `json:"profileDigest"`
	MaxAttestationLifetimeHours float64 `json:"maxAttestationLifetimeHours,omitempty"`
	Safe                        *struct {
		Owners    []string `json:"owners"`
		Threshold int      `json:"threshold"`
	} `json:"safe,omitempty"`
}

// Load requires all six deployment fields and rejects unknown fields.
// Lifetime and Safe settings are optional; no values are queried on-chain.
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

	var safe *Safe
	if f.Safe != nil {
		if !common.IsHexAddress(f.Auditor) {
			return nil, fmt.Errorf("policy: %s: auditor must be the Safe's address when safe is set, got %q", path, f.Auditor)
		}
		safe = &Safe{Threshold: f.Safe.Threshold}
		for _, raw := range f.Safe.Owners {
			if !common.IsHexAddress(raw) {
				return nil, fmt.Errorf("policy: %s: safe.owners entry %q is not an address", path, raw)
			}
			owner := common.HexToAddress(raw)
			if owner == (common.Address{}) || safe.IsOwner(owner) {
				return nil, fmt.Errorf("policy: %s: safe.owners entry %s is zero or listed twice", path, owner.Hex())
			}
			safe.Owners = append(safe.Owners, owner)
		}
		if len(safe.Owners) == 0 {
			return nil, fmt.Errorf("policy: %s: safe.owners must list at least one owner", path)
		}
		if safe.Threshold < 1 || safe.Threshold > len(safe.Owners) {
			return nil, fmt.Errorf("policy: %s: safe.threshold must be between 1 and the number of owners (%d), got %d", path, len(safe.Owners), safe.Threshold)
		}
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
		Safe:                   safe,
	}, nil
}
