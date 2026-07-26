package blockchain

import (
	"context"
	"crypto/ecdsa"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// Signer abstracts "produce a signed transaction for address X" so
// TxManager never needs raw private key material for backends that refuse
// to expose it by design (a KMS or Vault Transit key signs remotely; the
// key bytes never leave the service). internal/keys provides the
// local-keystore/vault/kms-mock implementations; this package only depends
// on the interface, not on internal/keys, to keep the dependency direction
// one-way.
//
// SubmitRequest.PrivateKey remains supported directly (StaticKeySigner is
// what it's adapted through internally) for simple local/dev/test use where
// a raw key is genuinely appropriate.
type Signer interface {
	// Address returns the account this Signer signs for. May do I/O (e.g. a
	// KMS DescribeKey call) so it takes a context.
	Address(ctx context.Context) (common.Address, error)
	// SignTx returns tx signed for chainID. Implementations MUST NOT mutate
	// tx's signature in place in a way that's visible to the caller's copy;
	// return the signed transaction as go-ethereum's types.SignTx does.
	SignTx(ctx context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)
}

// StaticKeySigner adapts a plain in-memory *ecdsa.PrivateKey to Signer. This
// is what SubmitRequest.PrivateKey is internally converted through, and is
// also the right choice for tests and any deployment that intentionally
// wants the simplest (least secure) key handling.
type StaticKeySigner struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

// NewStaticKeySigner wraps key as a Signer. Returns a nil Signer for a nil
// key, mirroring the pre-Signer convention (a nil *ecdsa.PrivateKey meant
// "this role's hot key isn't configured") so every existing call site that
// may legitimately pass nil (an optional hot key) keeps working unchanged.
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
