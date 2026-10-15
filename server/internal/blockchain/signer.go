package blockchain

import (
	"context"
	"crypto/ecdsa"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// Signer signs for one address without exposing key material to TxManager, supporting
// remote backends such as Vault Transit. internal/keys implements local-keystore, vault
// and kms-mock; blockchain depends only on this interface.
// SubmitRequest.PrivateKey supports local/dev/test keys through StaticKeySigner.
type Signer interface {
	// Address returns the signing account and may perform I/O, hence the context
	Address(ctx context.Context) (common.Address, error)
	// SignTx signs for chainID and must return a new transaction without mutating tx, like
	// types.SignTx
	SignTx(ctx context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)
}

// StaticKeySigner wraps an in-memory *ecdsa.PrivateKey for tests or deployments
// accepting raw keys in process memory
type StaticKeySigner struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

// NewStaticKeySigner wraps key, returning a nil Signer for an optional unconfigured key
func NewStaticKeySigner(key *ecdsa.PrivateKey) Signer {
	if key == nil {
		return nil
	}
	return StaticKeySigner{key: key, addr: crypto.PubkeyToAddress(key.PublicKey)}
}

func (s StaticKeySigner) Address(context.Context) (common.Address, error) { return s.addr, nil }

func (s StaticKeySigner) SignTx(_ context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
	return types.SignTx(tx, types.LatestSignerForChainID(chainID), s.key)
}
