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

// TestProviderSatisfiesBlockchainSigner is a compile-time check (it will
// fail to build, not just fail at runtime, if this ever regresses): every
// Provider this package returns must be directly usable as a
// blockchain.Signer without an adapter — that's the whole point of keeping
// the two interfaces structurally compatible so this package need not
// import internal/blockchain in production code (see keys.go's doc
// comment). cmd/platform/main.go's loadSigner relies on exactly this.
func TestProviderSatisfiesBlockchainSigner(t *testing.T) {
	p, err := Load("compliance", Config{Mode: ModeRaw, RawHexKey: testHexKey})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var _ blockchain.Signer = p
}
