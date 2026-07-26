package auditpkg

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/eip712"
)

func loadMintVector(t *testing.T) (eip712.Domain, eip712.MintAttestation, SignedResult) {
	t.Helper()
	p := filepath.Join("..", "..", "..", "shared", "vectors", "mint-eip712.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("golden vector not found: %v", err)
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
		Digest    string `json:"digest"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	amount, _ := new(big.Int).SetString(v.Message.Amount, 10)
	nonce, _ := new(big.Int).SetString(v.Message.Nonce, 10)

	domain := eip712.Domain{
		Name:              v.Domain.Name,
		Version:           v.Domain.Version,
		ChainID:           big.NewInt(v.Domain.ChainID),
		VerifyingContract: common.HexToAddress(v.Domain.VerifyingContract),
	}
	mint := eip712.MintAttestation{
		Auditor:        common.HexToAddress(v.Message.Auditor),
		ProfileDigest:  bytes32(t, v.Message.ProfileDigest),
		RecordKey:      bytes32(t, v.Message.RecordKey),
		MetadataDigest: bytes32(t, v.Message.MetadataDigest),
		Amount:         amount,
		Nonce:          nonce,
		ValidUntil:     v.Message.ValidUntil,
		Vault:          common.HexToAddress(v.Message.Vault),
	}
	sr := SignedResult{
		FormatVersion:   "1.0",
		Auditor:         v.Message.Auditor,
		PrimaryType:     "MintAttestation",
		TypedDataDigest: v.Digest,
		Signature:       v.Signature,
		SignedAt:        "2026-07-17T12:30:00Z",
	}
	return domain, mint, sr
}

func bytes32(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(s[2:])
	if err != nil || len(b) != 32 {
		t.Fatalf("bad bytes32 %q", s)
	}
	var out [32]byte
	copy(out[:], b)
	return out
}

func TestVerifyMintSignedResultAcceptsGoldenVector(t *testing.T) {
	domain, mint, sr := loadMintVector(t)
	if err := VerifyMintSignedResult(sr, domain, mint, mint.Auditor); err != nil {
		t.Fatalf("expected valid signed result, got error: %v", err)
	}
}

func TestVerifyMintSignedResultRejectsWrongConfiguredAuditor(t *testing.T) {
	domain, mint, sr := loadMintVector(t)
	wrongAuditor := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	if err := VerifyMintSignedResult(sr, domain, mint, wrongAuditor); err == nil {
		t.Fatal("expected error when configured auditor differs from signer")
	}
}

func TestVerifyMintSignedResultRejectsTamperedDigest(t *testing.T) {
	domain, mint, sr := loadMintVector(t)
	sr.TypedDataDigest = "0x" + hex.EncodeToString(make([]byte, 32)) // all-zero digest
	if err := VerifyMintSignedResult(sr, domain, mint, mint.Auditor); err == nil {
		t.Fatal("expected error for typedDataDigest not matching recomputed digest")
	}
}

func TestVerifyMintSignedResultRejectsTamperedAmount(t *testing.T) {
	domain, mint, sr := loadMintVector(t)
	mint.Amount = new(big.Int).Add(mint.Amount, big.NewInt(1)) // attacker bumps amount after signing
	if err := VerifyMintSignedResult(sr, domain, mint, mint.Auditor); err == nil {
		t.Fatal("expected error: recomputed digest must change when amount changes")
	}
}

func TestVerifyMintSignedResultRejectsWrongPrimaryType(t *testing.T) {
	domain, mint, sr := loadMintVector(t)
	sr.PrimaryType = "BurnAttestation"
	if err := VerifyMintSignedResult(sr, domain, mint, mint.Auditor); err == nil {
		t.Fatal("expected error for primaryType mismatch")
	}
}

func TestVerifyMintSignedResultRejectsBadFormatVersion(t *testing.T) {
	domain, mint, sr := loadMintVector(t)
	sr.FormatVersion = "9.9"
	if err := VerifyMintSignedResult(sr, domain, mint, mint.Auditor); err == nil {
		t.Fatal("expected error for unsupported formatVersion")
	}
}

func TestVerifyMintSignedResultRejectsDeclaredAuditorMismatch(t *testing.T) {
	domain, mint, sr := loadMintVector(t)
	sr.Auditor = "0x0000000000000000000000000000000000dEaD"
	if err := VerifyMintSignedResult(sr, domain, mint, mint.Auditor); err == nil {
		t.Fatal("expected error: declared auditor must equal recovered signer")
	}
}
