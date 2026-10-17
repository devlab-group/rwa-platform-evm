package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/dal/memory"
)

func TestAdminChallengeKeyedByNonce(t *testing.T) {
	ctx := context.Background()
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey).Hex()
	other, _ := crypto.GenerateKey()
	otherAddr := crypto.PubkeyToAddress(other.PublicKey).Hex()
	sign := func(msg string) []byte {
		sig, err := crypto.Sign(accounts.TextHash([]byte(msg)), key)
		if err != nil {
			t.Fatal(err)
		}
		return sig
	}
	newSvc := func(ttl time.Duration) *AdminChallengeService {
		return NewAdminChallengeService(memory.NewAdminChallengeRepository(), ttl, "Test")
	}

	t.Run("outstanding challenges coexist", func(t *testing.T) {
		svc := newSvc(time.Hour)
		n1, m1, _, err := svc.Create(ctx, addr)
		if err != nil {
			t.Fatal(err)
		}
		n2, m2, _, err := svc.Create(ctx, addr)
		if err != nil {
			t.Fatal(err)
		}
		if n1 == n2 {
			t.Fatal("nonces must differ")
		}
		// Both outstanding challenges remain usable
		if _, err := svc.Verify(ctx, addr, n1, sign(m1)); err != nil {
			t.Fatalf("first challenge: %v", err)
		}
		if _, err := svc.Verify(ctx, addr, n2, sign(m2)); err != nil {
			t.Fatalf("second challenge: %v", err)
		}
	})

	t.Run("nonce is single use", func(t *testing.T) {
		svc := newSvc(time.Hour)
		n, m, _, err := svc.Create(ctx, addr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Verify(ctx, addr, n, sign(m)); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Verify(ctx, addr, n, sign(m)); err != ErrAdminChallengeUsed {
			t.Fatalf("reuse err = %v, want ErrAdminChallengeUsed", err)
		}
	})

	t.Run("nonce of another address is rejected", func(t *testing.T) {
		svc := newSvc(time.Hour)
		n, m, _, err := svc.Create(ctx, addr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Verify(ctx, otherAddr, n, sign(m)); err != ErrAdminAddressMismatch {
			t.Fatalf("err = %v, want ErrAdminAddressMismatch", err)
		}
		// A rejected attempt leaves the challenge usable
		if _, err := svc.Verify(ctx, addr, n, sign(m)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unknown and expired nonces are rejected", func(t *testing.T) {
		svc := newSvc(-time.Second)
		n, m, _, err := svc.Create(ctx, addr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Verify(ctx, addr, n, sign(m)); err != ErrAdminChallengeExpired {
			t.Fatalf("expired err = %v", err)
		}
		if _, err := svc.Verify(ctx, addr, "nope", sign(m)); err != ErrAdminChallengeExpired {
			t.Fatalf("unknown err = %v", err)
		}
	})

	t.Run("concurrent use succeeds once", func(t *testing.T) {
		svc := newSvc(time.Hour)
		n, m, _, err := svc.Create(ctx, addr)
		if err != nil {
			t.Fatal(err)
		}
		sig := sign(m)
		var ok atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := svc.Verify(ctx, addr, n, sig); err == nil {
					ok.Add(1)
				}
			}()
		}
		wg.Wait()
		if ok.Load() != 1 {
			t.Fatalf("%d concurrent verifications succeeded, want 1", ok.Load())
		}
	})
}
