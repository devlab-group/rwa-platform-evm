package attestation

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Safe v1.3.0+ type strings for CompatibilityFallbackHandler's ERC-1271 message hash
// (shared/eip712/types.md)
const (
	SafeDomainTypeString  = "EIP712Domain(uint256 chainId,address verifyingContract)"
	SafeMessageTypeString = "SafeMessage(bytes message)"
)

var (
	safeDomainTypeHash  = crypto.Keccak256Hash([]byte(SafeDomainTypeString))
	safeMessageTypeHash = crypto.Keccak256Hash([]byte(SafeMessageTypeString))
)

// SafeDomainSeparator computes the chain/address domain returned by Safe.domainSeparator()
func SafeDomainSeparator(chainID *big.Int, safe common.Address) (common.Hash, error) {
	if chainID == nil || chainID.Sign() <= 0 {
		return common.Hash{}, errors.New("attestation: chainId must be positive")
	}
	chain, err := encodeUint256(chainID, "chainId")
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(safeDomainTypeHash.Bytes(), chain, common.LeftPadBytes(safe.Bytes(), 32)), nil
}

// SafeMessageHash wraps the attestation digest as a SafeMessage under the Safe's domain
func SafeMessageHash(chainID *big.Int, safe common.Address, digest common.Hash) (common.Hash, error) {
	sep, err := SafeDomainSeparator(chainID, safe)
	if err != nil {
		return common.Hash{}, err
	}
	// abi.encode(bytes32) is the digest's 32 bytes
	structHash := crypto.Keccak256Hash(safeMessageTypeHash.Bytes(), crypto.Keccak256(digest.Bytes()))
	return Digest(sep, structHash), nil
}

// CombineSafeSignatures sorts by owner address and concatenates 65-byte signatures.
// It requires v of 27/28; Safe reserves 0/1 for contract signatures and pre-approved hashes.
// The caller must verify signature recovery and owner membership.
func CombineSafeSignatures(sigs map[common.Address][]byte) ([]byte, error) {
	if len(sigs) == 0 {
		return nil, errors.New("attestation: no owner signatures to combine")
	}
	owners := make([]common.Address, 0, len(sigs))
	for owner := range sigs {
		owners = append(owners, owner)
	}
	slices.SortFunc(owners, func(a, b common.Address) int { return bytes.Compare(a[:], b[:]) })

	combined := make([]byte, 0, 65*len(owners))
	for _, owner := range owners {
		sig := sigs[owner]
		if len(sig) != 65 || (sig[64] != 27 && sig[64] != 28) {
			return nil, fmt.Errorf("attestation: signature for owner %s must be 65 bytes with v of 27 or 28", owner.Hex())
		}
		combined = append(combined, sig...)
	}
	return combined, nil
}
