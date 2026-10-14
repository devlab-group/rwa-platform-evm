package blockchain

import (
	"context"
	"crypto/ecdsa"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// Signer produces signed transactions for one address, so TxManager does not
// need raw private key material from backends that never expose it (a KMS or
// Vault Transit key signs remotely). internal/keys provides the
// local-keystore, vault and kms-mock implementations; this package depends only
// on the interface, not on internal/keys.
//
// SubmitRequest.PrivateKey is also supported for local, dev and test use, and
// is adapted through StaticKeySigner.
type Signer interface {
	// Address returns the account this Signer signs for. It may do I/O (for
	// example a KMS DescribeKey call), hence the context.
	Address(ctx context.Context) (common.Address, error)
	// SignTx returns tx signed for chainID. Like go-ethereum's types.SignTx, it
	// returns a new signed transaction and must not alter the caller's tx.
	SignTx(ctx context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)
}

// StaticKeySigner adapts an in-memory *ecdsa.PrivateKey to Signer. It suits
// tests and any setup that accepts a raw key in process memory.
type StaticKeySigner struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

// NewStaticKeySigner wraps key as a Signer. It returns a nil Signer for a nil
// key, so a caller with an optional, unconfigured hot key can pass it through
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
