package keys

import (
	"context"
	"testing"
)

func TestKMSMockDeterministicSeedIsReproducible(t *testing.T) {
	p1, err := Load("compliance", Config{Mode: ModeKMSMock, KMSMockSeedHex: "test-seed-1"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p2, err := Load("compliance", Config{Mode: ModeKMSMock, KMSMockSeedHex: "test-seed-1"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	addr1, _ := p1.Address(context.Background())
	addr2, _ := p2.Address(context.Background())
	if addr1 != addr2 {
		t.Errorf("same seed produced different addresses: %s vs %s", addr1.Hex(), addr2.Hex())
	}
}

func TestKMSMockDifferentRolesGetDifferentKeys(t *testing.T) {
	p1, err := Load("compliance", Config{Mode: ModeKMSMock, KMSMockSeedHex: "shared-seed"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p2, err := Load("pricer", Config{Mode: ModeKMSMock, KMSMockSeedHex: "shared-seed"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	addr1, _ := p1.Address(context.Background())
	addr2, _ := p2.Address(context.Background())
	if addr1 == addr2 {
		t.Error("compliance and pricer roles derived the same address from the same seed")
	}
}

func TestKMSMockEmptySeedIsRandomAndEphemeral(t *testing.T) {
	p1, err := Load("relayer", Config{Mode: ModeKMSMock})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p2, err := Load("relayer", Config{Mode: ModeKMSMock})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	addr1, _ := p1.Address(context.Background())
	addr2, _ := p2.Address(context.Background())
	if addr1 == addr2 {
		t.Error("two empty-seed Loads produced the same address; want independent random keys")
	}
}

// TestKMSMockCloseZeroesKey is the K2 regression test.
func TestKMSMockCloseZeroesKey(t *testing.T) {
	p, err := newKMSMockProvider("compliance", "test-seed")
	if err != nil {
		t.Fatalf("newKMSMockProvider: %v", err)
	}
	if p.key.D.Sign() == 0 {
		t.Fatal("test setup bug: key is already zero before Close")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if p.key.D.Sign() != 0 {
		t.Error("Close did not zero the private scalar")
	}
}
