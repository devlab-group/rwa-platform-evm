// Package repotest shares repository contract tests between in-memory and MongoDB adapters
package repotest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Run executes every contract suite against repos, which must start empty
func Run(t *testing.T, repos *repository.Repositories) {
	t.Run("Idempotency", func(t *testing.T) { idempotency(t, repos.Idempotency) })
	t.Run("AdminChallenge", func(t *testing.T) { adminChallenge(t, repos.AdminChallenges) })
	t.Run("WalletChallenge", func(t *testing.T) { walletChallenge(t, repos.WalletChallenges) })
	t.Run("WalletSession", func(t *testing.T) { walletSession(t, repos.WalletSessions) })
	t.Run("NonceLease", func(t *testing.T) { nonceLease(t, repos.NonceLeases) })
	t.Run("Transaction", func(t *testing.T) { transaction(t, repos.Transactions) })
	t.Run("AssetRecord", func(t *testing.T) { assetRecord(t, repos.AssetRecords) })
	t.Run("KYCClaim", func(t *testing.T) { kycClaim(t, repos.KYCEvents) })
}

// consumeConcurrently runs n simultaneous attempts and returns how many succeeded
func consumeConcurrently(n int, attempt func() bool) int {
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if attempt() {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return won
}

func idempotency(t *testing.T, repo repository.IdempotencyRepository) {
	ctx := context.Background()

	t.Run("live reservation hides its token and blocks a second owner", func(t *testing.T) {
		existing, reserved, token, err := repo.Reserve(ctx, "k-live", "POST", "/x", "hash-a", time.Hour)
		if err != nil || !reserved || token == "" || existing != nil {
			t.Fatalf("first Reserve = %v, %v, %q, %v; want nil, true, token, nil", existing, reserved, token, err)
		}
		existing, reserved, other, err := repo.Reserve(ctx, "k-live", "POST", "/x", "hash-b", time.Hour)
		if err != nil || reserved || other != "" {
			t.Fatalf("second Reserve = %v, %q, %v; want not reserved, no token", reserved, other, err)
		}
		if existing == nil || existing.RequestHash != "hash-a" || existing.ResponseStatus != 0 || existing.Token != "" {
			t.Errorf("existing = %+v, want the first owner's hash, status 0 and no token", existing)
		}
	})

	t.Run("complete stores the response once for the owner only", func(t *testing.T) {
		_, _, token, err := repo.Reserve(ctx, "k-done", "POST", "/x", "h", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Complete(ctx, "k-done", "wrong", 202, []byte("no")); !errors.Is(err, repository.ErrFencingTokenMismatch) {
			t.Fatalf("Complete with a foreign token = %v, want ErrFencingTokenMismatch", err)
		}
		if got, _ := repo.Get(ctx, "k-done"); got.ResponseStatus != 0 || len(got.ResponseBody) != 0 {
			t.Fatalf("a rejected Complete wrote %d %q", got.ResponseStatus, got.ResponseBody)
		}
		if err := repo.Complete(ctx, "k-done", token, 202, []byte(`{"ok":true}`)); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		existing, reserved, _, err := repo.Reserve(ctx, "k-done", "POST", "/x", "h", time.Hour)
		if err != nil || reserved {
			t.Fatalf("Reserve after Complete = reserved %v, %v; want replay", reserved, err)
		}
		if existing.ResponseStatus != 202 || string(existing.ResponseBody) != `{"ok":true}` {
			t.Errorf("replay = %d %q, want 202 and the stored body", existing.ResponseStatus, existing.ResponseBody)
		}
	})

	t.Run("complete and release of an unknown key are rejected", func(t *testing.T) {
		if err := repo.Complete(ctx, "k-none", "t", 200, nil); !errors.Is(err, repository.ErrFencingTokenMismatch) {
			t.Errorf("Complete unknown = %v, want ErrFencingTokenMismatch", err)
		}
		if err := repo.Release(ctx, "k-none", "t"); !errors.Is(err, repository.ErrFencingTokenMismatch) {
			t.Errorf("Release unknown = %v, want ErrFencingTokenMismatch", err)
		}
	})

	t.Run("release needs the owner's token and frees the key", func(t *testing.T) {
		_, _, token, _ := repo.Reserve(ctx, "k-rel", "POST", "/x", "h", time.Hour)
		if err := repo.Release(ctx, "k-rel", "wrong"); !errors.Is(err, repository.ErrFencingTokenMismatch) {
			t.Fatalf("Release with a foreign token = %v, want ErrFencingTokenMismatch", err)
		}
		if _, err := repo.Get(ctx, "k-rel"); err != nil {
			t.Fatalf("a rejected Release deleted the reservation: %v", err)
		}
		if err := repo.Release(ctx, "k-rel", token); err != nil {
			t.Fatalf("Release: %v", err)
		}
		_, reserved, next, err := repo.Reserve(ctx, "k-rel", "POST", "/x", "h", time.Hour)
		if err != nil || !reserved || next == "" || next == token {
			t.Errorf("Reserve after Release = %v, %q (old %q), %v; want a fresh token", reserved, next, token, err)
		}
	})

	t.Run("an expired reservation is taken over and the old owner is fenced out", func(t *testing.T) {
		_, reserved, old, err := repo.Reserve(ctx, "k-exp", "POST", "/x", "h", -time.Second)
		if err != nil || !reserved {
			t.Fatalf("expired Reserve = %v, %v", reserved, err)
		}
		_, reserved, fresh, err := repo.Reserve(ctx, "k-exp", "POST", "/x", "h", time.Hour)
		if err != nil || !reserved || fresh == "" || fresh == old {
			t.Fatalf("takeover = %v, %q (old %q), %v; want a new token", reserved, fresh, old, err)
		}
		if err := repo.Complete(ctx, "k-exp", old, 200, []byte("stale")); !errors.Is(err, repository.ErrFencingTokenMismatch) {
			t.Errorf("stale Complete = %v, want ErrFencingTokenMismatch", err)
		}
		if err := repo.Release(ctx, "k-exp", old); !errors.Is(err, repository.ErrFencingTokenMismatch) {
			t.Errorf("stale Release = %v, want ErrFencingTokenMismatch", err)
		}
		if err := repo.Complete(ctx, "k-exp", fresh, 200, []byte("fresh")); err != nil {
			t.Errorf("new owner's Complete: %v", err)
		}
	})

	t.Run("simultaneous reservations admit exactly one owner", func(t *testing.T) {
		won := consumeConcurrently(12, func() bool {
			_, reserved, _, err := repo.Reserve(ctx, "k-race", "POST", "/x", "h", time.Hour)
			return err == nil && reserved
		})
		if won != 1 {
			t.Errorf("%d concurrent Reserve calls won, want 1", won)
		}
	})
}

func adminChallenge(t *testing.T, repo repository.AdminChallengeRepository) {
	ctx := context.Background()
	live := &models.AdminChallenge{Nonce: "n-live", Address: "0xA", Message: "m", ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now()}
	if err := repo.Insert(ctx, live); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.Get(ctx, "n-live"); err != nil || got.Used || got.Address != "0xA" || got.Message != "m" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if err := repo.MarkUsed(ctx, "n-live"); err != nil {
		t.Fatalf("first MarkUsed: %v", err)
	}
	if err := repo.MarkUsed(ctx, "n-live"); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("replayed MarkUsed = %v, want ErrAlreadyExists", err)
	}
	if got, _ := repo.Get(ctx, "n-live"); !got.Used {
		t.Error("a consumed challenge reads back as unused")
	}
	if err := repo.MarkUsed(ctx, "n-none"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("MarkUsed unknown = %v, want ErrNotFound", err)
	}
	if err := repo.Insert(ctx, &models.AdminChallenge{Nonce: "n-old", Address: "0xA", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, "n-old"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("Get expired = %v, want ErrNotFound", err)
	}
	if err := repo.Insert(ctx, &models.AdminChallenge{Nonce: "n-race", Address: "0xA", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if won := consumeConcurrently(12, func() bool { return repo.MarkUsed(ctx, "n-race") == nil }); won != 1 {
		t.Errorf("%d concurrent MarkUsed calls succeeded, want 1", won)
	}
}

func walletChallenge(t *testing.T, repo repository.WalletChallengeRepository) {
	ctx := context.Background()
	now := time.Now()
	mk := func(id, addr string, exp time.Time) *models.WalletChallenge {
		return &models.WalletChallenge{ID: id, Address: addr, Nonce: id, Message: "m", ExpiresAt: exp, CreatedAt: now}
	}
	for _, c := range []*models.WalletChallenge{
		mk("w1", "0xA", now.Add(time.Hour)), mk("w2", "0xA", now.Add(time.Hour)),
		mk("w-old", "0xA", now.Add(-time.Hour)), mk("w-other", "0xB", now.Add(time.Hour)),
	} {
		if err := repo.Create(ctx, c); err != nil {
			t.Fatalf("Create %s: %v", c.ID, err)
		}
	}
	if err := repo.Create(ctx, mk("w1", "0xA", now.Add(time.Hour))); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("duplicate Create = %v, want ErrAlreadyExists", err)
	}
	if n, err := repo.CountActive(ctx, "0xA", now); err != nil || n != 2 {
		t.Errorf("CountActive = %d, %v; want 2 (expired and other-address rows excluded)", n, err)
	}
	if err := repo.MarkUsed(ctx, "w1"); err != nil {
		t.Fatalf("first MarkUsed: %v", err)
	}
	if err := repo.MarkUsed(ctx, "w1"); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("replayed MarkUsed = %v, want ErrAlreadyExists", err)
	}
	if n, _ := repo.CountActive(ctx, "0xA", now); n != 1 {
		t.Errorf("CountActive after a use = %d, want 1", n)
	}
	if err := repo.MarkUsed(ctx, "w-none"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("MarkUsed unknown = %v, want ErrNotFound", err)
	}
	if won := consumeConcurrently(12, func() bool { return repo.MarkUsed(ctx, "w2") == nil }); won != 1 {
		t.Errorf("%d concurrent MarkUsed calls succeeded, want 1", won)
	}
}

func walletSession(t *testing.T, repo repository.WalletSessionRepository) {
	ctx := context.Background()
	if err := repo.Create(ctx, &models.WalletSession{Token: "s-live", Address: "0xA", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.Get(ctx, "s-live"); err != nil || got.Address != "0xA" {
		t.Fatalf("Get live = %+v, %v", got, err)
	}
	if err := repo.Create(ctx, &models.WalletSession{Token: "s-old", Address: "0xA", ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, "s-old"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("Get expired = %v, want ErrNotFound", err)
	}
	if _, err := repo.Get(ctx, "s-none"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("Get unknown = %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, "s-live"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, "s-live"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("Get revoked = %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, "s-none"); err != nil {
		t.Errorf("Delete unknown = %v, want nil", err)
	}
}

func nonceLease(t *testing.T, repo repository.NonceLeaseRepository) {
	ctx := context.Background()
	tok1, ok, err := repo.Acquire(ctx, "signer", "h1", time.Hour)
	if err != nil || !ok || tok1 != 1 {
		t.Fatalf("first Acquire = %d, %v, %v; want 1, true", tok1, ok, err)
	}
	if tok, ok, err := repo.Acquire(ctx, "signer", "h1", time.Hour); err != nil || ok || tok != 0 {
		t.Errorf("Acquire of a live lease = %d, %v, %v; want 0, false (even for the holder)", tok, ok, err)
	}
	if ok, _ := repo.Renew(ctx, "signer", "h2", tok1, time.Hour); ok {
		t.Error("Renew by another holder succeeded")
	}
	if ok, _ := repo.Renew(ctx, "signer", "h1", tok1+5, time.Hour); ok {
		t.Error("Renew with a foreign token succeeded")
	}
	if ok, err := repo.Renew(ctx, "signer", "h1", tok1, time.Hour); err != nil || !ok {
		t.Errorf("Renew by the holder = %v, %v", ok, err)
	}
	if err := repo.Release(ctx, "signer", "h2", tok1); !errors.Is(err, repository.ErrFencingTokenMismatch) {
		t.Errorf("Release by another holder = %v, want ErrFencingTokenMismatch", err)
	}
	if _, ok, _ := repo.Acquire(ctx, "signer", "h2", time.Hour); ok {
		t.Error("a rejected Release freed the lease")
	}
	if err := repo.Release(ctx, "signer", "h1", tok1); err != nil {
		t.Fatalf("Release: %v", err)
	}
	tok2, ok, err := repo.Acquire(ctx, "signer", "h2", time.Hour)
	if err != nil || !ok || tok2 != tok1+1 {
		t.Fatalf("Acquire after Release = %d, %v, %v; want token %d", tok2, ok, err, tok1+1)
	}
	if ok, _ := repo.Renew(ctx, "signer", "h1", tok1, time.Hour); ok {
		t.Error("the superseded holder renewed")
	}
	if err := repo.Release(ctx, "signer", "h1", tok1); !errors.Is(err, repository.ErrFencingTokenMismatch) {
		t.Errorf("stale Release = %v, want ErrFencingTokenMismatch", err)
	}

	// expiry alone frees a lease, and tokens keep rising
	if _, ok, _ := repo.Acquire(ctx, "other", "h1", -time.Second); !ok {
		t.Fatal("Acquire with an already-expired ttl failed")
	}
	tok, ok, err := repo.Acquire(ctx, "other", "h2", time.Hour)
	if err != nil || !ok || tok != 2 {
		t.Errorf("takeover of an expired lease = %d, %v, %v; want token 2", tok, ok, err)
	}
	if won := consumeConcurrently(12, func() bool {
		_, ok, err := repo.Acquire(ctx, "race", "h", time.Hour)
		return err == nil && ok
	}); won != 1 {
		t.Errorf("%d concurrent Acquire calls won, want 1", won)
	}
}

func transaction(t *testing.T, repo repository.TransactionRepository) {
	ctx := context.Background()
	tx := &models.Transaction{ID: "tx-1", IdempotencyKey: "idem-1", Kind: "setStatus", ChainID: 31337, Status: models.TxPending, SubmittedAt: time.Now()}
	if err := repo.Create(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, tx); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("duplicate Create = %v, want ErrAlreadyExists", err)
	}

	t.Run("lookup by idempotency key", func(t *testing.T) {
		if got, err := repo.GetByIdempotencyKey(ctx, "idem-1"); err != nil || got.ID != "tx-1" {
			t.Errorf("GetByIdempotencyKey = %+v, %v", got, err)
		}
		if _, err := repo.GetByIdempotencyKey(ctx, "idem-none"); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("unknown key = %v, want ErrNotFound", err)
		}
		// event-derived rows carry no key and must never match the empty key
		if err := repo.Create(ctx, &models.Transaction{ID: "evt:0x1", ChainID: 31337, TxHash: "0x1", Status: models.TxConfirmed}); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.GetByIdempotencyKey(ctx, ""); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("empty key = %v, want ErrNotFound", err)
		}
	})

	t.Run("conditional update", func(t *testing.T) {
		upd := *tx
		upd.Status = models.TxMined
		if ok, err := repo.UpdateConditional(ctx, &upd, 0); err != nil || !ok {
			t.Fatalf("UpdateConditional at the stored version 0 = %v, %v; want true", ok, err)
		}
		got, _ := repo.Get(ctx, "tx-1")
		if got.Status != models.TxMined || got.Version != 1 {
			t.Fatalf("after update status/version = %s/%d, want mined/1", got.Status, got.Version)
		}
		stale := *tx
		stale.Status = models.TxFailed
		if ok, err := repo.UpdateConditional(ctx, &stale, 0); err != nil || ok {
			t.Fatalf("stale UpdateConditional = %v, %v; want false, nil", ok, err)
		}
		if got, _ := repo.Get(ctx, "tx-1"); got.Status != models.TxMined || got.Version != 1 {
			t.Errorf("a stale update wrote status/version %s/%d", got.Status, got.Version)
		}
		next := *got
		next.Status = models.TxConfirmed
		if ok, err := repo.UpdateConditional(ctx, &next, 1); err != nil || !ok {
			t.Fatalf("UpdateConditional at version 1 = %v, %v", ok, err)
		}
		if got, _ := repo.Get(ctx, "tx-1"); got.Version != 2 || got.Status != models.TxConfirmed {
			t.Errorf("version/status = %d/%s, want 2/confirmed", got.Version, got.Status)
		}
		ghost := &models.Transaction{ID: "tx-ghost"}
		if ok, err := repo.UpdateConditional(ctx, ghost, 0); ok || !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("UpdateConditional on a missing id = %v, %v; want false, ErrNotFound", ok, err)
		}
	})
}

func assetRecord(t *testing.T, repo repository.AssetRecordRepository) {
	ctx := context.Background()
	rec := &models.AssetRecord{RecordID: "rec-1", Status: models.RecordStatusSigned, Amount: "5", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := repo.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	upd := *rec
	upd.Nonce = "n1"
	if ok, err := repo.UpdateConditional(ctx, &upd, 0); err != nil || !ok {
		t.Fatalf("UpdateConditional at version 0 = %v, %v; want true", ok, err)
	}
	stale := *rec
	stale.Nonce = "stale"
	if ok, err := repo.UpdateConditional(ctx, &stale, 0); err != nil || ok {
		t.Fatalf("stale UpdateConditional = %v, %v; want false, nil", ok, err)
	}
	got, _ := repo.Get(ctx, "rec-1")
	if got.Nonce != "n1" || got.Version != 1 {
		t.Errorf("record nonce/version = %q/%d, want n1/1", got.Nonce, got.Version)
	}
	if ok, err := repo.UpdateConditional(ctx, &models.AssetRecord{RecordID: "rec-ghost"}, 0); ok || !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("missing record = %v, %v; want false, ErrNotFound", ok, err)
	}
	// two writers racing on one version: exactly one wins
	cur, _ := repo.Get(ctx, "rec-1")
	if won := consumeConcurrently(8, func() bool {
		c := *cur
		ok, err := repo.UpdateConditional(ctx, &c, cur.Version)
		return err == nil && ok
	}); won != 1 {
		t.Errorf("%d racing UpdateConditional calls won, want 1", won)
	}
}

func kycClaim(t *testing.T, repo repository.KYCEventRepository) {
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	claim := func(addr string, at time.Time, key string) bool {
		ok, err := repo.ClaimLatestForAddress(ctx, addr, at, key)
		if err != nil {
			t.Fatalf("claim %s %v %s: %v", addr, at, key, err)
		}
		return ok
	}
	if _, err := repo.CurrentClaimEventKey(ctx, "0xA"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("CurrentClaimEventKey before any claim = %v, want ErrNotFound", err)
	}
	steps := []struct {
		name string
		at   time.Time
		key  string
		want bool
	}{
		{"first claim wins", t0, "b", true},
		{"older decision loses", t0.Add(-time.Second), "z", false},
		{"same time, smaller key loses", t0, "a", false},
		{"identical claim loses", t0, "b", false},
		{"same time, greater key wins", t0, "c", true},
		{"later decision wins even with a smaller key", t0.Add(time.Second), "a", true},
	}
	for _, s := range steps {
		if got := claim("0xA", s.at, s.key); got != s.want {
			t.Errorf("%s: claim = %v, want %v", s.name, got, s.want)
		}
	}
	if k, err := repo.CurrentClaimEventKey(ctx, "0xA"); err != nil || k != "a" {
		t.Errorf("CurrentClaimEventKey = %q, %v; want the winning key a", k, err)
	}
	if !claim("0xB", t0.Add(-time.Hour), "x") {
		t.Error("claims are not independent per address")
	}

	// Race first inserts at one timestamp: the greatest key must win. Repeat with fresh
	// addresses to exercise insert conflicts.
	const n = 8
	for round := 0; round < 25; round++ {
		addr := fmt.Sprintf("0xC%02d", round)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = repo.ClaimLatestForAddress(ctx, addr, t0, string(rune('a'+i)))
			}()
		}
		wg.Wait()
		if k, _ := repo.CurrentClaimEventKey(ctx, addr); k != string(rune('a'+n-1)) {
			t.Fatalf("round %d: after concurrent claims the current key is %q, want %q", round, k, string(rune('a'+n-1)))
		}
	}
}
