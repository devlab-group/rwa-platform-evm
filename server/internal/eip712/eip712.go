// Package eip712 computes the RWA-Supply-Attestation EIP-712 domain
// separator, struct hashes, and digests, and recovers the signer of a
// digest. Type strings and field order are FROZEN by shared/eip712/types.md
// and MUST match byte-for-byte across Solidity, the Go signer, and this
// package. See shared/vectors/typehashes.json and mint-eip712.json.
package eip712

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Domain is the EIP-712 domain for SupplyController attestations.
type Domain struct {
	Name              string
	Version           string
	ChainID           *big.Int
	VerifyingContract common.Address
}

// MintAttestation mirrors ISupplyController.MintAttestation field order.
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

// BurnAttestation mirrors ISupplyController.BurnAttestation field order.
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

const (
	domainTypeString = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"
	mintTypeString   = "MintAttestation(address auditor,bytes32 profileDigest,bytes32 recordKey,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)"
	burnTypeString   = "BurnAttestation(address auditor,bytes32 profileDigest,bytes32 operationId,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)"
)

// DomainTypehash, MintTypehash, and BurnTypehash are exported so callers
// (and tests) can compare directly against shared/vectors/typehashes.json.
var (
	DomainTypehash = crypto.Keccak256Hash([]byte(domainTypeString))
	MintTypehash   = crypto.Keccak256Hash([]byte(mintTypeString))
	BurnTypehash   = crypto.Keccak256Hash([]byte(burnTypeString))
)

// word32 left-pads b to a 32-byte big-endian word, as abi.encode does for
// every static type used here (address, bytesN, uintN).
func word32(b []byte) [32]byte {
	var w [32]byte
	copy(w[32-len(b):], b)
	return w
}

func addressWord(a common.Address) [32]byte { return word32(a.Bytes()) }
func bytes32Word(b [32]byte) [32]byte       { return b }
func uintWord(n *big.Int) [32]byte {
	if n == nil {
		n = big.NewInt(0)
	}
	return word32(n.Bytes())
}
func uint64Word(n uint64) [32]byte { return word32(new(big.Int).SetUint64(n).Bytes()) }

// DomainSeparator computes keccak256(abi.encode(domainTypehash, keccak256(name),
// keccak256(version), chainId, verifyingContract)).
func DomainSeparator(d Domain) [32]byte {
	nameHash := crypto.Keccak256Hash([]byte(d.Name))
	versionHash := crypto.Keccak256Hash([]byte(d.Version))

	buf := make([]byte, 0, 32*5)
	buf = append(buf, DomainTypehash.Bytes()...)
	buf = append(buf, nameHash.Bytes()...)
	buf = append(buf, versionHash.Bytes()...)
	chainWord := uintWord(d.ChainID)
	buf = append(buf, chainWord[:]...)
	addrWord := addressWord(d.VerifyingContract)
	buf = append(buf, addrWord[:]...)

	return crypto.Keccak256Hash(buf)
}

// HashMintAttestation computes hashStruct(MintAttestation).
func HashMintAttestation(m MintAttestation) [32]byte {
	buf := make([]byte, 0, 32*9)
	buf = append(buf, MintTypehash.Bytes()...)
	w := addressWord(m.Auditor)
	buf = append(buf, w[:]...)
	w = bytes32Word(m.ProfileDigest)
	buf = append(buf, w[:]...)
	w = bytes32Word(m.RecordKey)
	buf = append(buf, w[:]...)
	w = bytes32Word(m.MetadataDigest)
	buf = append(buf, w[:]...)
	w = uintWord(m.Amount)
	buf = append(buf, w[:]...)
	w = uintWord(m.Nonce)
	buf = append(buf, w[:]...)
	w = uint64Word(m.ValidUntil)
	buf = append(buf, w[:]...)
	w = addressWord(m.Vault)
	buf = append(buf, w[:]...)
	return crypto.Keccak256Hash(buf)
}

// HashBurnAttestation computes hashStruct(BurnAttestation).
func HashBurnAttestation(b BurnAttestation) [32]byte {
	buf := make([]byte, 0, 32*9)
	buf = append(buf, BurnTypehash.Bytes()...)
	w := addressWord(b.Auditor)
	buf = append(buf, w[:]...)
	w = bytes32Word(b.ProfileDigest)
	buf = append(buf, w[:]...)
	w = bytes32Word(b.OperationID)
	buf = append(buf, w[:]...)
	w = bytes32Word(b.MetadataDigest)
	buf = append(buf, w[:]...)
	w = uintWord(b.Amount)
	buf = append(buf, w[:]...)
	w = uintWord(b.Nonce)
	buf = append(buf, w[:]...)
	w = uint64Word(b.ValidUntil)
	buf = append(buf, w[:]...)
	w = addressWord(b.Vault)
	buf = append(buf, w[:]...)
	return crypto.Keccak256Hash(buf)
}

// Digest computes keccak256(0x1901 || domainSeparator || hashStruct).
func Digest(domainSeparator, hashStruct [32]byte) [32]byte {
	buf := make([]byte, 0, 2+32+32)
	buf = append(buf, 0x19, 0x01)
	buf = append(buf, domainSeparator[:]...)
	buf = append(buf, hashStruct[:]...)
	return crypto.Keccak256Hash(buf)
}

// MintDigest is a convenience wrapper computing the full digest for a mint
// attestation under the given domain.
func MintDigest(d Domain, m MintAttestation) [32]byte {
	return Digest(DomainSeparator(d), HashMintAttestation(m))
}

// BurnDigest is a convenience wrapper computing the full digest for a burn
// attestation under the given domain.
func BurnDigest(d Domain, b BurnAttestation) [32]byte {
	return Digest(DomainSeparator(d), HashBurnAttestation(b))
}

// RecoverSigner recovers the EOA that produced signature over digest.
// signature MUST be the standard 65-byte [R || S || V] form with V in
// {0,1,27,28}; it is normalized to {0,1} for go-ethereum's recovery API.
func RecoverSigner(digest [32]byte, signature []byte) (common.Address, error) {
	if len(signature) != 65 {
		return common.Address{}, errors.New("eip712: signature must be 65 bytes")
	}
	sig := make([]byte, 65)
	copy(sig, signature)
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	if sig[64] != 0 && sig[64] != 1 {
		return common.Address{}, errors.New("eip712: invalid recovery id")
	}
	pub, err := crypto.SigToPub(digest[:], sig)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}
