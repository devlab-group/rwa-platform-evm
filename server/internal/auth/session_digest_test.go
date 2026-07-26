package auth

import (
	"context"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/memory"
)

// TestWalletSessionStoresDigestNotToken checks that
// the value persisted as the session's storage id is the token's SHA-256
// digest, never the token itself, so read access to the session collection
// cannot reconstruct a usable bearer credential.
func TestWalletSessionStoresDigestNotToken(t *testing.T) {
	repo := memory.NewWalletSessionRepository()
	sm := NewSessionManager(repo, time.Minute)
	ctx := context.Background()

	token, _, err := sm.Issue(ctx, "0xabc")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// The raw token must NOT be the stored id: an attacker who only reads the
	// collection sees the digest, and looking up by the raw token fails.
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

	// A legitimate holder's token still validates (Validate re-derives the
	// digest)...
	if addr, ok := sm.Validate(ctx, token); !ok || addr != "0xabc" {
		t.Fatalf("Validate(token) = %q,%v, want 0xabc,true", addr, ok)
	}
	// ...but an attacker who lifted the stored digest and replays it AS a
	// token cannot use it: Validate hashes it again, yielding a different key.
	if _, ok := sm.Validate(ctx, digest); ok {
		t.Fatal("replaying the stored digest as a bearer token must NOT validate")
	}
}
