package keys

import (
	"testing"

	"github.com/rwa-platform/server/internal/blockchain"
)

func TestLoadUnknownModeErrors(t *testing.T) {
	if _, err := Load("compliance", Config{Mode: "quantum-mock"}); err == nil {
		t.Fatal("expected an error for an unknown KEY_PROVIDER_MODE")
	}
}

// TestProviderSatisfiesBlockchainSigner is a compile-time check that a Provider
// is usable as a blockchain.Signer without an adapter, which loadSigner in
// cmd/platform relies on
func TestProviderSatisfiesBlockchainSigner(t *testing.T) {
	p, err := Load("compliance", Config{Mode: ModeRaw, RawHexKey: testHexKey})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var _ blockchain.Signer = p
}
