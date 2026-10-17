package attestation

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Reject out-of-range amounts, nonces and chain IDs instead of hashing wrapped or truncated values
func TestDigests_RefuseOutOfRangeNumbers(t *testing.T) {
	tooBig := new(big.Int).Lsh(big.NewInt(1), 256)
	maxUint := new(big.Int).Sub(tooBig, big.NewInt(1))
	good := Domain{ChainID: big.NewInt(31337)}

	tests := []struct {
		name    string
		domain  Domain
		amount  *big.Int
		nonce   *big.Int
		wantErr string
	}{
		{"nil chainId", Domain{}, big.NewInt(1), big.NewInt(1), "chainId must be positive"},
		{"zero chainId", Domain{ChainID: big.NewInt(0)}, big.NewInt(1), big.NewInt(1), "chainId must be positive"},
		{"negative chainId", Domain{ChainID: big.NewInt(-1)}, big.NewInt(1), big.NewInt(1), "chainId must be positive"},
		{"chainId above uint256", Domain{ChainID: tooBig}, big.NewInt(1), big.NewInt(1), "chainId exceeds uint256"},
		{"nil amount", good, nil, big.NewInt(1), "amount is nil"},
		{"negative amount", good, big.NewInt(-1), big.NewInt(1), "amount must not be negative"},
		{"amount above uint256", good, tooBig, big.NewInt(1), "amount exceeds uint256"},
		{"nil nonce", good, big.NewInt(1), nil, "nonce is nil"},
		{"negative nonce", good, big.NewInt(1), big.NewInt(-1), "nonce must not be negative"},
		{"nonce above uint256", good, big.NewInt(1), tooBig, "nonce exceeds uint256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := MintDigest(tt.domain, MintAttestation{Amount: tt.amount, Nonce: tt.nonce})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("MintDigest error = %v, want it to contain %q", err, tt.wantErr)
			}
			_, err = BurnDigest(tt.domain, BurnAttestation{Amount: tt.amount, Nonce: tt.nonce})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("BurnDigest error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}

	// The largest legal values are accepted
	if _, err := MintDigest(Domain{ChainID: maxUint}, MintAttestation{Amount: maxUint, Nonce: maxUint}); err != nil {
		t.Errorf("MintDigest at uint256 max: %v", err)
	}
	if _, err := BurnDigest(Domain{ChainID: maxUint}, BurnAttestation{Amount: maxUint, Nonce: maxUint}); err != nil {
		t.Errorf("BurnDigest at uint256 max: %v", err)
	}
}

func TestSign_RefusesNilKey(t *testing.T) {
	if sig, err := Sign(common.Hash{1}, nil); err == nil || !strings.Contains(err.Error(), "signing key is nil") {
		t.Errorf("Sign(nil key) = %x, %v; want a nil-key error", sig, err)
	}
}

func TestRecover_RefusesBadSignatures(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	digest := common.Hash{7}
	sig, err := Sign(digest, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(digest, sig[:64]); err == nil || !strings.Contains(err.Error(), "must be 65 bytes") {
		t.Errorf("short signature error = %v", err)
	}
	// V of 2 is neither 27/28 nor 0/1
	bad := append([]byte{}, sig...)
	bad[64] = 2
	if _, err := Recover(digest, bad); err == nil {
		t.Error("Recover accepted V=2")
	}
}
