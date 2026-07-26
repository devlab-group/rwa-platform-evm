package keys

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// kmsMockProvider is an explicit, clearly-labeled stand-in for a real KMS
// (AWS KMS, GCP Cloud KMS, ...) so the Provider abstraction and every call
// site that depends on it can be exercised in CI/local dev without a real
// KMS account or credentials. NEVER use in production — Load logs a loud
// warning every time this mode is selected.
//
// With a non-empty seed, the per-role key is deterministic
// (keccak256(seed || role)), so the same seed always reproduces the same
// address across restarts — useful for a repeatable local/CI environment.
// An empty seed generates a random ephemeral key and logs its address once;
// that address will NOT survive a restart, so on-chain role grants made
// against it become stale immediately — fine for a one-off smoke test,
// useless for anything longer-lived.
type kmsMockProvider struct {
	role string
	seed string

	mu   sync.RWMutex
	key  *ecdsa.PrivateKey
	addr common.Address
}

func newKMSMockProvider(role, seedHex string) (*kmsMockProvider, error) {
	log.Printf("keys: WARNING — role %q is using kms-mock, a non-production stand-in for a real KMS. Never use this mode in production.", role)
	p := &kmsMockProvider{role: role, seed: seedHex}
	if err := p.derive(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *kmsMockProvider) derive() error {
	var key *ecdsa.PrivateKey
	if p.seed == "" {
		k, err := crypto.GenerateKey()
		if err != nil {
			return fmt.Errorf("keys: kms-mock: generating ephemeral key: %w", err)
		}
		key = k
		log.Printf("keys: kms-mock: role %q got a random ephemeral key (address %s) — will NOT survive a restart", p.role, crypto.PubkeyToAddress(key.PublicKey).Hex())
	} else {
		digest := crypto.Keccak256([]byte(p.seed), []byte(p.role))
		k, err := crypto.ToECDSA(digest)
		if err != nil {
			return fmt.Errorf("keys: kms-mock: deriving key for role %q: %w", p.role, err)
		}
		key = k
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.key = key
	p.addr = crypto.PubkeyToAddress(key.PublicKey)
	return nil
}

func (p *kmsMockProvider) Address(context.Context) (common.Address, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.addr, nil
}

func (p *kmsMockProvider) SignTx(_ context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return types.SignTx(tx, types.LatestSignerForChainID(chainID), p.key)
}

// Reload re-derives the key from the same seed (a no-op for a deterministic
// seed; regenerates a new random key, logged again, for an empty one).
func (p *kmsMockProvider) Reload(context.Context) error {
	return p.derive()
}

// Close zeroes the in-memory private key. See Provider.Close's doc comment.
func (p *kmsMockProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	zeroECDSAKey(p.key)
	return nil
}
