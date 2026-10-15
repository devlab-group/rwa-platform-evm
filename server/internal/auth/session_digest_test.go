package auth

import (
	"context"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/memory"
)

// TestWalletSessionStoresDigestNotToken checks that session storage contains only the
// SHA-256 lookup digest, not a usable credential
func TestWalletSessionStoresDigestNotToken(t *testing.T) {
	repo := memory.NewWalletSessionRepository()
	sm := NewSessionManager(repo, time.Minute)
	ctx := context.Background()

	token, _, err := sm.Issue(ctx, "0xabc")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// The raw token is not the stored id, so a lookup by it fails
	if _, err := repo.Get(ctx, token); err == nil {
		t.Fatal("raw token must not be a valid storage id (it must be stored only as a digest)")
	}
	digest := hashSessionToken(token)
	if digest == token {
		t.Fatal("digest must differ from the raw token")
	}
	if _, err := repo.Get(ctx, digest); err != nil {
		t.Fatalf("the digest is the storage id and must resolve: %v", err)
	}

	// The holder's token validates, because Validate re-derives the digest
	if addr, ok := sm.Validate(ctx, token); !ok || addr != "0xabc" {
		t.Fatalf("Validate(token) = %q,%v, want 0xabc,true", addr, ok)
	}
	// A stored digest replayed as a token fails, because Validate hashes it
	// again into a different key
	if _, ok := sm.Validate(ctx, digest); ok {
		t.Fatal("replaying the stored digest as a bearer token must NOT validate")
	}
}
