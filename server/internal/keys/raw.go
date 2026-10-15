package keys

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// rawKeyProvider retains the original scalar bytes and parsed key. Reload re-parses
// those bytes, so it cannot rotate to another key. Close wipes both mutable forms but
// cannot scrub the caller's Config hex string.
type rawKeyProvider struct {
	mu   sync.RWMutex
	raw  []byte
	key  *ecdsa.PrivateKey
	addr common.Address
}

func newRawKeyProvider(hexKey string) (*rawKeyProvider, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(hexKey, "0x"))
	if err != nil {
		return nil, fmt.Errorf("keys: raw mode: invalid hex private key: %w", err)
	}
	p := &rawKeyProvider{raw: raw}
	if err := p.parse(); err != nil {
		zeroBytes(p.raw)
		return nil, err
	}
	return p, nil
}

func (p *rawKeyProvider) parse() error {
	key, err := crypto.ToECDSA(p.raw)
	if err != nil {
		return fmt.Errorf("keys: raw mode: invalid hex private key: %w", err)
	}
	p.key = key
	p.addr = crypto.PubkeyToAddress(key.PublicKey)
	return nil
}

func (p *rawKeyProvider) Address(context.Context) (common.Address, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.addr, nil
}

func (p *rawKeyProvider) SignTx(_ context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return types.SignTx(tx, types.LatestSignerForChainID(chainID), p.key)
}

func (p *rawKeyProvider) Reload(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.parse()
}

// Close zeroes the parsed scalar and the raw bytes Reload would re-parse
func (p *rawKeyProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	zeroECDSAKey(p.key)
	zeroBytes(p.raw)
	return nil
}
