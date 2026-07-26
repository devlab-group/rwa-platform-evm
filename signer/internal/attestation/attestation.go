// Package attestation implements the EIP-712 domain separator, type hashes,
// struct hashes, and digest construction for MintAttestation and
// BurnAttestation, following the types in shared/eip712/types.md.
//
// All struct fields are EVM value types (address, bytesN, uintN), so
// abi.encode(...) is exactly the concatenation of each field's 32-byte
// left-padded word; no dynamic-type ABI encoding is required here.
package attestation

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Canonical EIP-712 type strings. These must match shared/eip712/types.md
// byte-for-byte; they are the single source of truth for the type hashes.
const (
	DomainTypeString = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"
	MintTypeString   = "MintAttestation(address auditor,bytes32 profileDigest,bytes32 recordKey,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)"
	BurnTypeString   = "BurnAttestation(address auditor,bytes32 profileDigest,bytes32 operationId,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)"

	DomainName    = "RWA-Supply-Attestation"
	DomainVersion = "1"
)

var (
	domainTypeHash = crypto.Keccak256Hash([]byte(DomainTypeString))
	mintTypeHash   = crypto.Keccak256Hash([]byte(MintTypeString))
	burnTypeHash   = crypto.Keccak256Hash([]byte(BurnTypeString))
	domainNameHash = crypto.Keccak256Hash([]byte(DomainName))
	domainVerHash  = crypto.Keccak256Hash([]byte(DomainVersion))
)

// DomainTypeHash, MintTypeHash, BurnTypeHash expose the type hashes
// (see shared/vectors/typehashes.json) for diagnostics and tests.
func DomainTypeHash() common.Hash { return domainTypeHash }
func MintTypeHash() common.Hash   { return mintTypeHash }
func BurnTypeHash() common.Hash   { return burnTypeHash }

// Domain identifies the chain and verifying contract an attestation is
// bound to.
type Domain struct {
	ChainID           *big.Int
	VerifyingContract common.Address
}

// Separator computes the EIP-712 domain separator for RWA-Supply-Attestation
// version "1" on d.ChainID / d.VerifyingContract.
func (d Domain) Separator() (common.Hash, error) {
	if d.ChainID == nil || d.ChainID.Sign() <= 0 {
		return common.Hash{}, errors.New("attestation: chainId must be positive")
	}
	if d.ChainID.BitLen() > 256 {
		return common.Hash{}, errors.New("attestation: chainId exceeds uint256")
	}
	buf := make([]byte, 0, 32*5)
	buf = append(buf, domainTypeHash.Bytes()...)
	buf = append(buf, domainNameHash.Bytes()...)
	buf = append(buf, domainVerHash.Bytes()...)
	buf = append(buf, common.LeftPadBytes(d.ChainID.Bytes(), 32)...)
	buf = append(buf, common.LeftPadBytes(d.VerifyingContract.Bytes(), 32)...)
	return crypto.Keccak256Hash(buf), nil
}

// MintAttestation mirrors the Solidity MintAttestation struct. Field order
// matters and must not change.
type MintAttestation struct {
	Auditor        common.Address
	ProfileDigest  [32]byte
	RecordKey      [32]byte
	MetadataDigest [32]byte
	Amount         *big.Int
	Nonce          *big.Int
	ValidUntil     uint64
	Vault          common.Address
}

// BurnAttestation mirrors the Solidity BurnAttestation struct. Field order
// matters and must not change.
type BurnAttestation struct {
	Auditor        common.Address
	ProfileDigest  [32]byte
	OperationID    [32]byte
	MetadataDigest [32]byte
	Amount         *big.Int
	Nonce          *big.Int
	ValidUntil     uint64
	Vault          common.Address
}

func encodeUint256(v *big.Int, field string) ([]byte, error) {
	if v == nil {
		return nil, fmt.Errorf("attestation: %s is nil", field)
	}
	if v.Sign() < 0 {
		return nil, fmt.Errorf("attestation: %s must not be negative", field)
	}
	if v.BitLen() > 256 {
		return nil, fmt.Errorf("attestation: %s exceeds uint256", field)
	}
	return common.LeftPadBytes(v.Bytes(), 32), nil
}

// HashStruct computes keccak256(abi.encode(MINT_ATTESTATION_TYPEHASH, ...)).
func (m MintAttestation) HashStruct() (common.Hash, error) {
	amount, err := encodeUint256(m.Amount, "amount")
	if err != nil {
		return common.Hash{}, err
	}
	nonce, err := encodeUint256(m.Nonce, "nonce")
	if err != nil {
		return common.Hash{}, err
	}
	buf := make([]byte, 0, 32*8)
	buf = append(buf, mintTypeHash.Bytes()...)
	buf = append(buf, common.LeftPadBytes(m.Auditor.Bytes(), 32)...)
	buf = append(buf, m.ProfileDigest[:]...)
	buf = append(buf, m.RecordKey[:]...)
	buf = append(buf, m.MetadataDigest[:]...)
	buf = append(buf, amount...)
	buf = append(buf, nonce...)
	buf = append(buf, common.LeftPadBytes(new(big.Int).SetUint64(m.ValidUntil).Bytes(), 32)...) // uint64 as full 32-byte word
	buf = append(buf, common.LeftPadBytes(m.Vault.Bytes(), 32)...)
	return crypto.Keccak256Hash(buf), nil
}

// HashStruct computes keccak256(abi.encode(BURN_ATTESTATION_TYPEHASH, ...)).
func (b BurnAttestation) HashStruct() (common.Hash, error) {
	amount, err := encodeUint256(b.Amount, "amount")
	if err != nil {
		return common.Hash{}, err
	}
	nonce, err := encodeUint256(b.Nonce, "nonce")
	if err != nil {
		return common.Hash{}, err
	}
	buf := make([]byte, 0, 32*8)
	buf = append(buf, burnTypeHash.Bytes()...)
	buf = append(buf, common.LeftPadBytes(b.Auditor.Bytes(), 32)...)
	buf = append(buf, b.ProfileDigest[:]...)
	buf = append(buf, b.OperationID[:]...)
	buf = append(buf, b.MetadataDigest[:]...)
	buf = append(buf, amount...)
	buf = append(buf, nonce...)
	buf = append(buf, common.LeftPadBytes(new(big.Int).SetUint64(b.ValidUntil).Bytes(), 32)...)
	buf = append(buf, common.LeftPadBytes(b.Vault.Bytes(), 32)...)
	return crypto.Keccak256Hash(buf), nil
}

// Digest computes the final EIP-712 digest:
// keccak256(0x1901 || domainSeparator || hashStruct).
func Digest(domainSeparator, hashStruct common.Hash) common.Hash {
	buf := make([]byte, 0, 2+32+32)
	buf = append(buf, 0x19, 0x01)
	buf = append(buf, domainSeparator.Bytes()...)
	buf = append(buf, hashStruct.Bytes()...)
	return crypto.Keccak256Hash(buf)
}

// MintDigest is a convenience wrapper computing the full EIP-712 digest for
// a MintAttestation under domain d.
func MintDigest(d Domain, m MintAttestation) (common.Hash, error) {
	sep, err := d.Separator()
	if err != nil {
		return common.Hash{}, err
	}
	hs, err := m.HashStruct()
	if err != nil {
		return common.Hash{}, err
	}
	return Digest(sep, hs), nil
}

// BurnDigest is a convenience wrapper computing the full EIP-712 digest for
// a BurnAttestation under domain d.
func BurnDigest(d Domain, b BurnAttestation) (common.Hash, error) {
	sep, err := d.Separator()
	if err != nil {
		return common.Hash{}, err
	}
	hs, err := b.HashStruct()
	if err != nil {
		return common.Hash{}, err
	}
	return Digest(sep, hs), nil
}

// RecordKey computes keccak256(bytes(recordId)), the mint-side unique key
// derived from the metadata record's recordId.
func RecordKey(recordID string) [32]byte {
	return crypto.Keccak256Hash([]byte(recordID))
}

// Sign signs the raw 32-byte EIP-712 digest (no EIP-191 personal-message
// prefix) with key, returning the standard 65-byte [R || S || V] Ethereum
// signature with V in {27, 28}.
func Sign(digest common.Hash, key *ecdsa.PrivateKey) ([]byte, error) {
	if key == nil {
		return nil, errors.New("attestation: signing key is nil")
	}
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		return nil, fmt.Errorf("attestation: sign: %w", err)
	}
	// go-ethereum's crypto.Sign returns V in {0,1}; the wire/on-chain
	// convention (and the golden vector) uses V in {27,28}.
	sig[64] += 27
	return sig, nil
}

// Recover recovers the signer address from a 65-byte [R || S || V]
// signature (V in {27,28} or {0,1}) over digest.
func Recover(digest common.Hash, sig []byte) (common.Address, error) {
	if len(sig) != 65 {
		return common.Address{}, fmt.Errorf("attestation: signature must be 65 bytes, got %d", len(sig))
	}
	normalized := make([]byte, 65)
	copy(normalized, sig)
	if normalized[64] >= 27 {
		normalized[64] -= 27
	}
	pub, err := crypto.SigToPub(digest.Bytes(), normalized)
	if err != nil {
		return common.Address{}, fmt.Errorf("attestation: recover: %w", err)
	}
	return crypto.PubkeyToAddress(*pub), nil
}
