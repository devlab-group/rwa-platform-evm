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

// kmsMockProvider is a stand-in for a real KMS, for CI and local development.
// It is not for production, and newKMSMockProvider logs a warning each time it
// is constructed.
//
// With a non-empty seed, the per-role key is keccak256(seed || role), so the
// address is the same across restarts. An empty seed generates a random key
// and logs its address; that address changes on every restart, so on-chain role
// grants made to it go stale.
type kmsMockProvider struct {
	role string
	seed string

	mu   sync.RWMutex
	key  *ecdsa.PrivateKey
	addr common.Address
}

func newKMSMockProvider(role, seedHex string) (*kmsMockProvider, error) {
	log.Printf("keys: WARNING - role %q is using kms-mock, a non-production stand-in for a real KMS. Never use this mode in production.", role)
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
		log.Printf("keys: kms-mock: role %q got a random ephemeral key (address %s) - will NOT survive a restart", p.role, crypto.PubkeyToAddress(key.PublicKey).Hex())
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

// Reload re-derives the key from the seed. With an empty seed it generates a
// new random key and logs the new address
func (p *kmsMockProvider) Reload(context.Context) error {
	return p.derive()
}

// Close zeroes the in-memory private key
func (p *kmsMockProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	zeroECDSAKey(p.key)
	return nil
}
