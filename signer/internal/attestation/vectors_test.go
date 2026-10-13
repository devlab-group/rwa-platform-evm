package attestation

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type typehashesVector struct {
	DomainTypehash    string `json:"domainTypehash"`
	DomainNameHash    string `json:"domainNameHash"`
	DomainVersionHash string `json:"domainVersionHash"`
	MintTypehash      string `json:"mintTypehash"`
	BurnTypehash      string `json:"burnTypehash"`
}

type mintVector struct {
	Domain struct {
		Name              string `json:"name"`
		Version           string `json:"version"`
		ChainID           int64  `json:"chainId"`
		VerifyingContract string `json:"verifyingContract"`
	} `json:"domain"`
	Message struct {
		Auditor        string `json:"auditor"`
		ProfileDigest  string `json:"profileDigest"`
		RecordID       string `json:"recordId"`
		RecordKey      string `json:"recordKey"`
		MetadataDigest string `json:"metadataDigest"`
		Amount         string `json:"amount"`
		Nonce          string `json:"nonce"`
		ValidUntil     uint64 `json:"validUntil"`
		Vault          string `json:"vault"`
	} `json:"message"`
	DomainSeparator  string `json:"domainSeparator"`
	HashStruct       string `json:"hashStruct"`
	Digest           string `json:"digest"`
	SignerPrivateKey string `json:"signerPrivateKey"`
	Signature        string `json:"signature"`
}

// sharedVectorsDir locates shared/vectors relative to this test file
func sharedVectorsDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "shared", "vectors"))
	if err != nil {
		t.Fatalf("resolving shared/vectors path: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("shared/vectors not found at %s: %v", dir, err)
	}
	return dir
}

func loadJSON[T any](t *testing.T, path string) T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return v
}

func mustHash32(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		t.Fatalf("decoding hash %q: %v", s, err)
	}
	if len(b) != 32 {
		t.Fatalf("hash %q is %d bytes, want 32", s, len(b))
	}
	var out [32]byte
	copy(out[:], b)
	return out
}

func mustBigInt(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("parsing decimal integer %q", s)
	}
	return v
}

// TestVectors reproduces the type hashes, domain, struct hash, digest and exact signature
// from shared/vectors/typehashes.json and shared/vectors/mint-eip712.json
func TestVectors(t *testing.T) {
	dir := sharedVectorsDir(t)

	th := loadJSON[typehashesVector](t, filepath.Join(dir, "typehashes.json"))

	if got, want := DomainTypeHash().Hex(), th.DomainTypehash; !strings.EqualFold(got, want) {
		t.Errorf("domain type hash = %s, want %s", got, want)
	}
	if got, want := crypto.Keccak256Hash([]byte(DomainName)).Hex(), th.DomainNameHash; !strings.EqualFold(got, want) {
		t.Errorf("domain name hash = %s, want %s", got, want)
	}
	if got, want := crypto.Keccak256Hash([]byte(DomainVersion)).Hex(), th.DomainVersionHash; !strings.EqualFold(got, want) {
		t.Errorf("domain version hash = %s, want %s", got, want)
	}
	if got, want := MintTypeHash().Hex(), th.MintTypehash; !strings.EqualFold(got, want) {
		t.Errorf("mint type hash = %s, want %s", got, want)
	}
	if got, want := BurnTypeHash().Hex(), th.BurnTypehash; !strings.EqualFold(got, want) {
		t.Errorf("burn type hash = %s, want %s", got, want)
	}

	mv := loadJSON[mintVector](t, filepath.Join(dir, "mint-eip712.json"))

	if mv.Domain.Name != DomainName {
		t.Fatalf("vector domain name = %q, want %q", mv.Domain.Name, DomainName)
	}
	if mv.Domain.Version != DomainVersion {
		t.Fatalf("vector domain version = %q, want %q", mv.Domain.Version, DomainVersion)
	}

	domain := Domain{
		ChainID:           big.NewInt(mv.Domain.ChainID),
		VerifyingContract: common.HexToAddress(mv.Domain.VerifyingContract),
	}
	sep, err := domain.Separator()
	if err != nil {
		t.Fatalf("Domain.Separator: %v", err)
	}
	if got, want := sep.Hex(), mv.DomainSeparator; !strings.EqualFold(got, want) {
		t.Errorf("domainSeparator = %s, want %s", got, want)
	}

	msg := MintAttestation{
		Auditor:        common.HexToAddress(mv.Message.Auditor),
		ProfileDigest:  mustHash32(t, mv.Message.ProfileDigest),
		RecordKey:      mustHash32(t, mv.Message.RecordKey),
		MetadataDigest: mustHash32(t, mv.Message.MetadataDigest),
		Amount:         mustBigInt(t, mv.Message.Amount),
		Nonce:          mustBigInt(t, mv.Message.Nonce),
		ValidUntil:     mv.Message.ValidUntil,
		Vault:          common.HexToAddress(mv.Message.Vault),
	}

	// Derive recordKey independently from recordId
	if got, want := RecordKey(mv.Message.RecordID), mustHash32(t, mv.Message.RecordKey); got != want {
		t.Errorf("RecordKey(%q) = 0x%x, want 0x%x", mv.Message.RecordID, got, want)
	}

	hs, err := msg.HashStruct()
	if err != nil {
		t.Fatalf("HashStruct: %v", err)
	}
	if got, want := hs.Hex(), mv.HashStruct; !strings.EqualFold(got, want) {
		t.Errorf("hashStruct = %s, want %s", got, want)
	}

	digest := Digest(sep, hs)
	if got, want := digest.Hex(), mv.Digest; !strings.EqualFold(got, want) {
		t.Errorf("digest = %s, want %s", got, want)
	}

	// Check the digest wrapper against the same vector
	digest2, err := MintDigest(domain, msg)
	if err != nil {
		t.Fatalf("MintDigest: %v", err)
	}
	if digest2 != digest {
		t.Errorf("MintDigest = %s, want %s", digest2.Hex(), digest.Hex())
	}

	keyBytes, err := hex.DecodeString(strings.TrimPrefix(mv.SignerPrivateKey, "0x"))
	if err != nil {
		t.Fatalf("decoding signerPrivateKey: %v", err)
	}
	privKey, err := crypto.ToECDSA(keyBytes)
	if err != nil {
		t.Fatalf("ToECDSA: %v", err)
	}

	sig, err := Sign(digest, privKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if got, want := "0x"+hex.EncodeToString(sig), mv.Signature; !strings.EqualFold(got, want) {
		t.Errorf("signature = %s, want %s (deterministic ECDSA must reproduce the golden signature exactly)", got, want)
	}

	recovered, err := Recover(digest, sig)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if want := common.HexToAddress(mv.Message.Auditor); recovered != want {
		t.Errorf("recovered signer = %s, want auditor %s", recovered.Hex(), want.Hex())
	}
}

// TestVectors_BurnTypeHashIndependentlyDerivable hashes the canonical burn type string
// from shared/eip712/types.md independently of the golden vector
func TestVectors_BurnTypeHashIndependentlyDerivable(t *testing.T) {
	want := crypto.Keccak256Hash([]byte(BurnTypeString))
	if BurnTypeHash() != want {
		t.Fatalf("BurnTypeHash() = %s, want %s", BurnTypeHash().Hex(), want.Hex())
	}
}

func TestDomain_RejectsNonPositiveChainID(t *testing.T) {
	d := Domain{ChainID: big.NewInt(0), VerifyingContract: common.Address{}}
	if _, err := d.Separator(); err == nil {
		t.Fatal("expected error for zero chainId")
	}
	d.ChainID = big.NewInt(-1)
	if _, err := d.Separator(); err == nil {
		t.Fatal("expected error for negative chainId")
	}
}

func TestHashStruct_RejectsNilAmount(t *testing.T) {
	m := MintAttestation{Nonce: big.NewInt(1)}
	if _, err := m.HashStruct(); err == nil {
		t.Fatal("expected error for nil amount")
	}
}

func TestHashStruct_RejectsNegativeAmount(t *testing.T) {
	m := MintAttestation{Amount: big.NewInt(-1), Nonce: big.NewInt(1)}
	if _, err := m.HashStruct(); err == nil {
		t.Fatal("expected error for negative amount")
	}
}

// TestSign_Deterministic requires identical signatures for the same key and digest
// (RFC 6979), supporting exact golden-vector comparisons
func TestSign_Deterministic(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	digest := crypto.Keccak256Hash([]byte("deterministic-sign-test"))
	sig1, err := Sign(digest, key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	sig2, err := Sign(digest, key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if hex.EncodeToString(sig1) != hex.EncodeToString(sig2) {
		t.Fatalf("signatures differ across calls: %x vs %x", sig1, sig2)
	}
}

type safeAuditorVector struct {
	Safe struct {
		Address string   `json:"address"`
		Owners  []string `json:"owners"`
	} `json:"safe"`
	Domain struct {
		ChainID           int64  `json:"chainId"`
		VerifyingContract string `json:"verifyingContract"`
	} `json:"domain"`
	Message struct {
		Auditor        string `json:"auditor"`
		ProfileDigest  string `json:"profileDigest"`
		RecordID       string `json:"recordId"`
		RecordKey      string `json:"recordKey"`
		MetadataDigest string `json:"metadataDigest"`
		Amount         string `json:"amount"`
		Nonce          string `json:"nonce"`
		ValidUntil     uint64 `json:"validUntil"`
		Vault          string `json:"vault"`
	} `json:"message"`
	Digest              string `json:"digest"`
	SafeDomainTypehash  string `json:"safeDomainTypehash"`
	SafeMessageTypehash string `json:"safeMessageTypehash"`
	SafeDomainSeparator string `json:"safeDomainSeparator"`
	SafeMessageHash     string `json:"safeMessageHash"`
	OwnerSignatures     []struct {
		Owner      string `json:"owner"`
		PrivateKey string `json:"privateKey"`
		Signature  string `json:"signature"`
	} `json:"ownerSignatures"`
	CombinedSignature string `json:"combinedSignature"`
}

// TestVectors_SafeAuditor reproduces the hashes, owner signatures and combined bytes
// in shared/vectors/safe-auditor.json without deploying a Safe
func TestVectors_SafeAuditor(t *testing.T) {
	v := loadJSON[safeAuditorVector](t, filepath.Join(sharedVectorsDir(t), "safe-auditor.json"))

	if got := crypto.Keccak256Hash([]byte(SafeDomainTypeString)).Hex(); !strings.EqualFold(got, v.SafeDomainTypehash) {
		t.Errorf("safe domain type hash = %s, want %s", got, v.SafeDomainTypehash)
	}
	if got := crypto.Keccak256Hash([]byte(SafeMessageTypeString)).Hex(); !strings.EqualFold(got, v.SafeMessageTypehash) {
		t.Errorf("safe message type hash = %s, want %s", got, v.SafeMessageTypehash)
	}

	chainID := big.NewInt(v.Domain.ChainID)
	digest, err := MintDigest(
		Domain{ChainID: chainID, VerifyingContract: common.HexToAddress(v.Domain.VerifyingContract)},
		MintAttestation{
			Auditor:        common.HexToAddress(v.Message.Auditor),
			ProfileDigest:  mustHash32(t, v.Message.ProfileDigest),
			RecordKey:      RecordKey(v.Message.RecordID),
			MetadataDigest: mustHash32(t, v.Message.MetadataDigest),
			Amount:         mustBigInt(t, v.Message.Amount),
			Nonce:          mustBigInt(t, v.Message.Nonce),
			ValidUntil:     v.Message.ValidUntil,
			Vault:          common.HexToAddress(v.Message.Vault),
		})
	if err != nil {
		t.Fatalf("MintDigest: %v", err)
	}
	if !strings.EqualFold(digest.Hex(), v.Digest) {
		t.Fatalf("digest = %s, want %s", digest.Hex(), v.Digest)
	}

	safe := common.HexToAddress(v.Safe.Address)
	sep, err := SafeDomainSeparator(chainID, safe)
	if err != nil {
		t.Fatalf("SafeDomainSeparator: %v", err)
	}
	if !strings.EqualFold(sep.Hex(), v.SafeDomainSeparator) {
		t.Errorf("safe domain separator = %s, want %s", sep.Hex(), v.SafeDomainSeparator)
	}
	msgHash, err := SafeMessageHash(chainID, safe, digest)
	if err != nil {
		t.Fatalf("SafeMessageHash: %v", err)
	}
	if !strings.EqualFold(msgHash.Hex(), v.SafeMessageHash) {
		t.Fatalf("safe message hash = %s, want %s", msgHash.Hex(), v.SafeMessageHash)
	}

	sigs := map[common.Address][]byte{}
	for _, ownerSig := range v.OwnerSignatures {
		key, err := crypto.HexToECDSA(strings.TrimPrefix(ownerSig.PrivateKey, "0x"))
		if err != nil {
			t.Fatalf("parsing owner key: %v", err)
		}
		sig, err := Sign(msgHash, key)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if got := "0x" + hex.EncodeToString(sig); !strings.EqualFold(got, ownerSig.Signature) {
			t.Errorf("owner %s signature = %s, want %s", ownerSig.Owner, got, ownerSig.Signature)
		}
		if signer, err := Recover(msgHash, sig); err != nil || signer != common.HexToAddress(ownerSig.Owner) {
			t.Errorf("owner signature recovers to %s (err %v), want %s", signer.Hex(), err, ownerSig.Owner)
		}
		sigs[common.HexToAddress(ownerSig.Owner)] = sig
	}

	combined, err := CombineSafeSignatures(sigs)
	if err != nil {
		t.Fatalf("CombineSafeSignatures: %v", err)
	}
	if got := "0x" + hex.EncodeToString(combined); !strings.EqualFold(got, v.CombinedSignature) {
		t.Errorf("combined signature = %s, want %s", got, v.CombinedSignature)
	}
}

func TestCombineSafeSignatures_RejectsNonECDSAv(t *testing.T) {
	sig := make([]byte, 65)
	sig[64] = 1 // a Safe reads v=1 as a pre-approved hash, not an ECDSA signature
	if _, err := CombineSafeSignatures(map[common.Address][]byte{{0x01}: sig}); err == nil {
		t.Fatal("expected an error for v=1")
	}
	if _, err := CombineSafeSignatures(nil); err == nil {
		t.Fatal("expected an error for no signatures")
	}
}
