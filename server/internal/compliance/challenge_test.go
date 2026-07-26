package compliance

import (
	"context"
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

	c, _ := svc.Create(context.Background(), addr.Hex())
	hash := accounts.TextHash([]byte(c.Message))
	sig, _ := crypto.Sign(hash, priv)

	if _, err := svc.Verify(context.Background(), c.Nonce, sig); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if _, err := svc.Verify(context.Background(), c.Nonce, sig); err != ErrChallengeUsed {
		t.Fatalf("replay verify: err = %v, want ErrChallengeUsed", err)
	}
}

// TestChallengeVerifyConcurrentReplaySucceedsOnce is the MarkUsed CAS
// regression test: N goroutines Verify the exact same (nonce, signature)
// pair at once. Exactly one may observe success; every other must get
// ErrChallengeUsed, never a duplicate success — see
// WalletChallengeRepository.MarkUsed's doc comment for why an
// unconditional "set used=true" isn't enough on its own.
func TestChallengeVerifyConcurrentReplaySucceedsOnce(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, _ := newTestChallengeService(time.Hour)

	c, _ := svc.Create(context.Background(), addr.Hex())
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

	c, _ := svc.Create(context.Background(), addr.Hex())
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

	c, _ := svc.Create(context.Background(), addr.Hex())
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

	// Caller claims a different address than the one that actually signed.
	if _, err := svc.VerifyOwnership(context.Background(), otherAddr, c.Nonce, sig); err != ErrAddressMismatch {
		t.Fatalf("err = %v, want ErrAddressMismatch", err)
	}
}

// TestChallengeCreateEnforcesActiveCap covers the active-challenge cap: any
// unauthenticated caller can create a new challenge for any valid address,
// and every call makes a new nonce/document, so the number of ACTIVE
// (unexpired, unused) challenges per normalized address is capped. Once an
// address has MaxActiveChallengesPerAddress currently-active challenges
// outstanding, one more Create must be rejected rather than growing
// storage unboundedly.
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

	// A DIFFERENT address must be unaffected by another address's cap.
	otherPriv, _ := crypto.GenerateKey()
	otherAddr := crypto.PubkeyToAddress(otherPriv.PublicKey)
	if _, err := svc.Create(context.Background(), otherAddr.Hex()); err != nil {
		t.Fatalf("Create for a different address: %v", err)
	}
}

// TestChallengeCreateCapCountsOnlyActiveChallenges pins the "unexpired,
// unused" qualifier on the cap: a challenge that has already been
// USED must free up a cap slot for a fresh Create, exactly like an
// expired one would (the cap counts standing storage exposure, not
// lifetime request volume).
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

	// Use the first challenge — it should no longer count as active.
	hash := accounts.TextHash([]byte(c.Message))
	sig, _ := crypto.Sign(hash, priv)
	if _, err := svc.Verify(context.Background(), c.Nonce, sig); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if _, err := svc.Create(context.Background(), addr.Hex()); err != nil {
		t.Fatalf("Create after the only active challenge was used: %v", err)
	}
}

// TestChallengeCreateCapExpiredChallengesDontCount pins the "unexpired"
// half of the same qualifier: a challenge whose TTL has already elapsed
// must not keep occupying a cap slot forever.
func TestChallengeCreateCapExpiredChallengesDontCount(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, _ := newTestChallengeService(10 * time.Millisecond)
	svc.MaxActiveChallengesPerAddress = 1

	if _, err := svc.Create(context.Background(), addr.Hex()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(context.Background(), addr.Hex()); err != ErrTooManyActiveChallenges {
		t.Fatalf("second Create before expiry: err = %v, want ErrTooManyActiveChallenges", err)
	}

	time.Sleep(20 * time.Millisecond) // past ttl
	if _, err := svc.Create(context.Background(), addr.Hex()); err != nil {
		t.Fatalf("Create after the only active challenge expired: %v", err)
	}
}

// TestWalletChallengeRepositoryCountActive exercises the memory repository
// method directly, independent of ChallengeService's cap enforcement above.
func TestWalletChallengeRepositoryCountActive(t *testing.T) {
	repo := memory.NewWalletChallengeRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	mustCreate := func(id, address string, expiresAt time.Time, used bool) {
		t.Helper()
		if err := repo.Create(ctx, &models.WalletChallenge{
			ID: id, Address: address, Nonce: id, ExpiresAt: expiresAt, Used: used, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	const addrA = "0xAAAA000000000000000000000000000000AAAA"
	const addrB = "0xBBBB000000000000000000000000000000BBBB"
	mustCreate("active-1", addrA, now.Add(time.Hour), false)
	mustCreate("active-2", addrA, now.Add(time.Hour), false)
	mustCreate("used", addrA, now.Add(time.Hour), true)
	mustCreate("expired", addrA, now.Add(-time.Hour), false)
	mustCreate("other-address", addrB, now.Add(time.Hour), false)

	n, err := repo.CountActive(ctx, addrA, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("CountActive(addrA) = %d, want 2 (used and expired must not count)", n)
	}

	n, err = repo.CountActive(ctx, addrB, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("CountActive(addrB) = %d, want 1", n)
	}
}

func TestVerifyOwnershipPreservesExistingComplianceStatus(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	svc, _, investors := newTestChallengeService(time.Hour)

	// Investor already has a compliance status from a prior manual action.
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
