package compliance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

func newTestChallengeService(ttl time.Duration) (*ChallengeService, *memory.WalletChallengeRepository, *memory.InvestorRepository) {
	repo := memory.NewWalletChallengeRepository()
	investors := memory.NewInvestorRepository()
	return NewChallengeService(repo, investors, ttl, "RWA Platform"), repo, investors
}

func TestChallengeCreateAndVerify(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	addr := crypto.PubkeyToAddress(priv.PublicKey)

	svc, _, _ := newTestChallengeService(time.Hour)

	c, err := svc.Create(context.Background(), addr.Hex())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.Used {
		t.Fatal("new challenge must not be used")
	}
	if c.ID != c.Nonce {
		t.Errorf("ID = %q, want it to equal Nonce %q (challenges are looked up by nonce)", c.ID, c.Nonce)
	}

	hash := accounts.TextHash([]byte(c.Message))
	sig, err := crypto.Sign(hash, priv)
	if err != nil {
		t.Fatal(err)
	}

	recovered, err := svc.Verify(context.Background(), c.Nonce, sig)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if recovered != addr {
		t.Errorf("recovered %s, want %s", recovered.Hex(), addr.Hex())
	}
}

func TestChallengeVerifyRejectsReplay(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, _ := newTestChallengeService(time.Hour)

	c, err := svc.Create(context.Background(), addr.Hex())

	if err != nil {

		t.Fatal(err)

	}
	hash := accounts.TextHash([]byte(c.Message))
	sig, _ := crypto.Sign(hash, priv)

	if _, err := svc.Verify(context.Background(), c.Nonce, sig); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if _, err := svc.Verify(context.Background(), c.Nonce, sig); err != ErrChallengeUsed {
		t.Fatalf("replay verify: err = %v, want ErrChallengeUsed", err)
	}
}

// TestChallengeVerifyConcurrentReplaySucceedsOnce checks concurrent verification of one
// nonce/signature: exactly one succeeds; others return ErrChallengeUsed through atomic
// MarkUsed
func TestChallengeVerifyConcurrentReplaySucceedsOnce(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, _ := newTestChallengeService(time.Hour)

	c, err := svc.Create(context.Background(), addr.Hex())

	if err != nil {

		t.Fatal(err)

	}
	hash := accounts.TextHash([]byte(c.Message))
	sig, _ := crypto.Sign(hash, priv)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := svc.Verify(context.Background(), c.Nonce, sig)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	successes, usedErrors := 0, 0
	for _, err := range errs {
		switch err {
		case nil:
			successes++
		case ErrChallengeUsed:
			usedErrors++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Errorf("successes = %d, want exactly 1", successes)
	}
	if usedErrors != n-1 {
		t.Errorf("ErrChallengeUsed count = %d, want %d", usedErrors, n-1)
	}
}

func TestChallengeVerifyRejectsExpired(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, _ := newTestChallengeService(-time.Minute) // already expired

	c, err := svc.Create(context.Background(), addr.Hex())

	if err != nil {

		t.Fatal(err)

	}
	hash := accounts.TextHash([]byte(c.Message))
	sig, _ := crypto.Sign(hash, priv)

	if _, err := svc.Verify(context.Background(), c.Nonce, sig); err != ErrChallengeExpired {
		t.Fatalf("err = %v, want ErrChallengeExpired", err)
	}
}

func TestChallengeVerifyRejectsWrongSigner(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	otherKey, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, _ := newTestChallengeService(time.Hour)

	c, err := svc.Create(context.Background(), addr.Hex())

	if err != nil {

		t.Fatal(err)

	}
	hash := accounts.TextHash([]byte(c.Message))
	sig, _ := crypto.Sign(hash, otherKey) // signed by a different key than the challenge address

	if _, err := svc.Verify(context.Background(), c.Nonce, sig); err != ErrAddressMismatch {
		t.Fatalf("err = %v, want ErrAddressMismatch", err)
	}
}

func TestChallengeCreateRejectsInvalidAddress(t *testing.T) {
	svc, _, _ := newTestChallengeService(time.Hour)
	if _, err := svc.Create(context.Background(), "not-an-address"); err == nil {
		t.Fatal("expected error for invalid address")
	}
}

func TestVerifyOwnershipRecordsInvestorFlag(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, investors := newTestChallengeService(time.Hour)

	c, err := svc.Create(context.Background(), addr.Hex())
	if err != nil {
		t.Fatal(err)
	}
	hash := accounts.TextHash([]byte(c.Message))
	sig, _ := crypto.Sign(hash, priv)

	inv, err := svc.VerifyOwnership(context.Background(), addr.Hex(), c.Nonce, sig)
	if err != nil {
		t.Fatalf("VerifyOwnership: %v", err)
	}
	if !inv.OwnershipVerified {
		t.Fatal("expected OwnershipVerified = true")
	}

	stored, err := investors.Get(context.Background(), addr.Hex())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !stored.OwnershipVerified {
		t.Fatal("expected persisted investor record to have OwnershipVerified = true")
	}
}

func TestVerifyOwnershipRejectsAddressMismatch(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	otherAddr := "0x000000000000000000000000000000000000A1"
	svc, _, _ := newTestChallengeService(time.Hour)

	c, err := svc.Create(context.Background(), addr.Hex())
	if err != nil {
		t.Fatal(err)
	}
	hash := accounts.TextHash([]byte(c.Message))
	sig, _ := crypto.Sign(hash, priv)

	// The caller claims a different address than the one that signed
	if _, err := svc.VerifyOwnership(context.Background(), otherAddr, c.Nonce, sig); err != ErrAddressMismatch {
		t.Fatalf("err = %v, want ErrAddressMismatch", err)
	}
}

// TestChallengeCreateEnforcesActiveCap checks that once an address has
// MaxActiveChallengesPerAddress unused, unexpired challenges, one more Create
// is rejected
func TestChallengeCreateEnforcesActiveCap(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, _ := newTestChallengeService(time.Hour)
	svc.MaxActiveChallengesPerAddress = 3

	for i := 0; i < 3; i++ {
		if _, err := svc.Create(context.Background(), addr.Hex()); err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
	}
	if _, err := svc.Create(context.Background(), addr.Hex()); err != ErrTooManyActiveChallenges {
		t.Fatalf("4th Create: err = %v, want ErrTooManyActiveChallenges", err)
	}

	// A different address is unaffected by another address's cap
	otherPriv, _ := crypto.GenerateKey()
	otherAddr := crypto.PubkeyToAddress(otherPriv.PublicKey)
	if _, err := svc.Create(context.Background(), otherAddr.Hex()); err != nil {
		t.Fatalf("Create for a different address: %v", err)
	}
}

// TestChallengeCreateCapCountsOnlyActiveChallenges checks that a used challenge
// frees a cap slot, as an expired one does
func TestChallengeCreateCapCountsOnlyActiveChallenges(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, _ := newTestChallengeService(time.Hour)
	svc.MaxActiveChallengesPerAddress = 1

	c, err := svc.Create(context.Background(), addr.Hex())
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(context.Background(), addr.Hex()); err != ErrTooManyActiveChallenges {
		t.Fatalf("second Create while first is still active: err = %v, want ErrTooManyActiveChallenges", err)
	}

	// Use the first challenge, which should then stop counting as active
	hash := accounts.TextHash([]byte(c.Message))
	sig, _ := crypto.Sign(hash, priv)
	if _, err := svc.Verify(context.Background(), c.Nonce, sig); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if _, err := svc.Create(context.Background(), addr.Hex()); err != nil {
		t.Fatalf("Create after the only active challenge was used: %v", err)
	}
}

// TestChallengeCreateCapExpiredChallengesDontCount checks that an expired
// challenge does not occupy a cap slot
func TestChallengeCreateCapExpiredChallengesDontCount(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, _ := newTestChallengeService(time.Minute)
	clock := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }
	svc.MaxActiveChallengesPerAddress = 1

	if _, err := svc.Create(context.Background(), addr.Hex()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(context.Background(), addr.Hex()); err != ErrTooManyActiveChallenges {
		t.Fatalf("second Create before expiry: err = %v, want ErrTooManyActiveChallenges", err)
	}

	clock = clock.Add(time.Minute + time.Second) // past ttl
	if _, err := svc.Create(context.Background(), addr.Hex()); err != nil {
		t.Fatalf("Create after the only active challenge expired: %v", err)
	}
}

func TestVerifyOwnershipPreservesExistingComplianceStatus(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, investors := newTestChallengeService(time.Hour)

	// The investor already has a compliance status from an earlier manual action
	if err := investors.Upsert(context.Background(), &models.Investor{Address: addr.Hex(), Status: models.ComplianceAllowed}); err != nil {
		t.Fatal(err)
	}

	c, err := svc.Create(context.Background(), addr.Hex())
	if err != nil {
		t.Fatal(err)
	}
	hash := accounts.TextHash([]byte(c.Message))
	sig, _ := crypto.Sign(hash, priv)

	inv, err := svc.VerifyOwnership(context.Background(), addr.Hex(), c.Nonce, sig)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Status != "Allowed" {
		t.Errorf("Status = %s, want Allowed to be preserved", inv.Status)
	}
	if !inv.OwnershipVerified {
		t.Error("expected OwnershipVerified = true")
	}
}

// Verify accepts a challenge up to and including its expiry instant and rejects it one
// second later
func TestChallengeVerify_ExpiryBoundary(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	issued := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		at      time.Time
		wantErr error
	}{
		{"one second before expiry", issued.Add(time.Minute - time.Second), nil},
		{"exactly at expiry", issued.Add(time.Minute), nil},
		{"one second after expiry", issued.Add(time.Minute + time.Second), ErrChallengeExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _ := newTestChallengeService(time.Minute)
			clock := issued
			svc.now = func() time.Time { return clock }
			ch, err := svc.Create(context.Background(), addr.Hex())
			if err != nil {
				t.Fatal(err)
			}
			sig, err := crypto.Sign(accounts.TextHash([]byte(ch.Message)), priv)
			if err != nil {
				t.Fatal(err)
			}
			clock = tt.at
			got, err := svc.Verify(context.Background(), ch.Nonce, sig)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Verify error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && got != addr {
				t.Errorf("Verify signer = %s, want %s", got, addr)
			}
		})
	}
}
