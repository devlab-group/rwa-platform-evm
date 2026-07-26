package eip712

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// repoRoot walks up from the test's working directory to find shared/vectors.
// Tests run with cwd == package directory, so shared/ lives four levels up
// (internal/eip712 -> internal -> server -> repo root).
func vectorPath(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "..", "shared", "vectors", name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("shared vectors not found at %s (%v); skipping golden-vector test", p, err)
	}
	return p
}

func hexToBytes32(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(trim0x(s))
	if err != nil {
		t.Fatalf("invalid hex %q: %v", s, err)
	}
	var out [32]byte
	if len(b) != 32 {
		t.Fatalf("expected 32 bytes, got %d for %q", len(b), s)
	}
	copy(out[:], b)
	return out
}

func trim0x(s string) string {
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		return s[2:]
	}
	return s
}

func TestTypehashesMatchVectors(t *testing.T) {
	path := vectorPath(t, "typehashes.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v struct {
		DomainTypehash string `json:"domainTypehash"`
		MintTypehash   string `json:"mintTypehash"`
		BurnTypehash   string `json:"burnTypehash"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got, want := "0x"+hex.EncodeToString(DomainTypehash.Bytes()), v.DomainTypehash; got != want {
		t.Errorf("DomainTypehash = %s, want %s", got, want)
	}
	if got, want := "0x"+hex.EncodeToString(MintTypehash.Bytes()), v.MintTypehash; got != want {
		t.Errorf("MintTypehash = %s, want %s", got, want)
	}
	if got, want := "0x"+hex.EncodeToString(BurnTypehash.Bytes()), v.BurnTypehash; got != want {
		t.Errorf("BurnTypehash = %s, want %s", got, want)
	}
}

func TestMintDigestMatchesGoldenVector(t *testing.T) {
	path := vectorPath(t, "mint-eip712.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v struct {
		Domain struct {
			Name              string `json:"name"`
			Version           string `json:"version"`
			ChainID           int64  `json:"chainId"`
			VerifyingContract string `json:"verifyingContract"`
		} `json:"domain"`
		Message struct {
			Auditor        string `json:"auditor"`
			ProfileDigest  string `json:"profileDigest"`
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
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	amount, ok := new(big.Int).SetString(v.Message.Amount, 10)
	if !ok {
		t.Fatalf("bad amount %q", v.Message.Amount)
	}
	nonce, ok := new(big.Int).SetString(v.Message.Nonce, 10)
	if !ok {
		t.Fatalf("bad nonce %q", v.Message.Nonce)
	}

	d := Domain{
		Name:              v.Domain.Name,
		Version:           v.Domain.Version,
		ChainID:           big.NewInt(v.Domain.ChainID),
		VerifyingContract: common.HexToAddress(v.Domain.VerifyingContract),
	}
	m := MintAttestation{
		Auditor:        common.HexToAddress(v.Message.Auditor),
		ProfileDigest:  hexToBytes32(t, v.Message.ProfileDigest),
		RecordKey:      hexToBytes32(t, v.Message.RecordKey),
		MetadataDigest: hexToBytes32(t, v.Message.MetadataDigest),
		Amount:         amount,
		Nonce:          nonce,
		ValidUntil:     v.Message.ValidUntil,
		Vault:          common.HexToAddress(v.Message.Vault),
	}

	gotDomainSep := DomainSeparator(d)
	if got, want := "0x"+hex.EncodeToString(gotDomainSep[:]), v.DomainSeparator; got != want {
		t.Errorf("DomainSeparator = %s, want %s", got, want)
	}

	gotHashStruct := HashMintAttestation(m)
	if got, want := "0x"+hex.EncodeToString(gotHashStruct[:]), v.HashStruct; got != want {
		t.Errorf("HashStruct = %s, want %s", got, want)
	}

	gotDigest := Digest(gotDomainSep, gotHashStruct)
	if got, want := "0x"+hex.EncodeToString(gotDigest[:]), v.Digest; got != want {
		t.Fatalf("Digest = %s, want %s", got, want)
	}

	// Recompute the signature from the vector's private key and confirm
	// RecoverSigner recovers the auditor address, proving digest correctness
	// end-to-end (not just against the precomputed intermediate hashes).
	privBytes, err := hex.DecodeString(trim0x(v.SignerPrivateKey))
	if err != nil {
		t.Fatalf("bad private key hex: %v", err)
	}
	priv, err := crypto.ToECDSA(privBytes)
	if err != nil {
		t.Fatalf("ToECDSA: %v", err)
	}
	sig, err := crypto.Sign(gotDigest[:], priv)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	recovered, err := RecoverSigner(gotDigest, sig)
	if err != nil {
		t.Fatalf("RecoverSigner: %v", err)
	}
	wantAddr := crypto.PubkeyToAddress(priv.PublicKey)
	if recovered != wantAddr {
		t.Errorf("recovered = %s, want %s", recovered.Hex(), wantAddr.Hex())
	}
	if recovered != m.Auditor {
		t.Errorf("recovered = %s, want auditor %s", recovered.Hex(), m.Auditor.Hex())
	}

	// Also verify against the vector's own pre-computed signature bytes.
	sigBytes, err := hex.DecodeString(trim0x(v.Signature))
	if err != nil {
		t.Fatalf("bad signature hex: %v", err)
	}
	recovered2, err := RecoverSigner(gotDigest, sigBytes)
	if err != nil {
		t.Fatalf("RecoverSigner(vector signature): %v", err)
	}
	if recovered2 != m.Auditor {
		t.Errorf("vector signature recovered = %s, want auditor %s", recovered2.Hex(), m.Auditor.Hex())
	}
}

func TestRecoverSignerRejectsWrongLength(t *testing.T) {
	var digest [32]byte
	if _, err := RecoverSigner(digest, []byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for short signature")
	}
}

// sanity: ensure our own key derivation and signing round-trip works even
// without the golden vector file present.
func TestSelfSignedRoundTrip(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	d := Domain{
		Name:              "RWA-Supply-Attestation",
		Version:           "1",
		ChainID:           big.NewInt(31337),
		VerifyingContract: common.HexToAddress("0x5FbDB2315678afecb367f032d93F642f64180aa"),
	}
	m := MintAttestation{
		Auditor:        crypto.PubkeyToAddress(priv.PublicKey),
		ProfileDigest:  [32]byte{1},
		RecordKey:      [32]byte{2},
		MetadataDigest: [32]byte{3},
		Amount:         big.NewInt(1000),
		Nonce:          big.NewInt(7),
		ValidUntil:     uint64(2000000000),
		Vault:          common.HexToAddress("0x0000000000000000000000000000000000dEaD"),
	}
	digest := MintDigest(d, m)
	sig, err := crypto.Sign(digest[:], priv)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverSigner(digest, sig)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != m.Auditor {
		t.Fatalf("recovered %s want %s", recovered.Hex(), m.Auditor.Hex())
	}
}
