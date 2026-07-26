package api

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/dal/models"
)

// personalSign produces an EIP-191 personal_sign signature over msg with key,
// hex-encoded with a 0x prefix — the exact shape a wallet submits to
// POST /auth/session.
func personalSign(t *testing.T, msg string, key *ecdsa.PrivateKey) string {
	t.Helper()
	sig, err := crypto.Sign(accounts.TextHash([]byte(msg)), key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27 // wallet convention: V in {27,28}; RecoverSigner normalizes either way
	return "0x" + hex.EncodeToString(sig)
}

// requestChallenge runs POST /auth/challenge for address and returns the
// message the wallet must sign.
func requestChallenge(t *testing.T, env *testEnv, address string) string {
	t.Helper()
	w := doJSON(t, env.router, http.MethodPost, "/auth/challenge", map[string]string{"address": address}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("challenge status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Message   string `json:"message"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Message == "" || resp.ExpiresAt == 0 {
		t.Fatalf("bad challenge response: %+v", resp)
	}
	return resp.Message
}

// TestAdminAuthChallengeSessionFlow is the wallet-signature -> JWT login
// regression: the admin requests a challenge, personal_signs it, exchanges the
// signature for a JWT, and that JWT authorizes an admin-only route.
func TestAdminAuthChallengeSessionFlow(t *testing.T) {
	env := setupTestApp(t)

	msg := requestChallenge(t, env, env.adminAddr.Hex())
	sig := personalSign(t, msg, env.adminKey)

	w := doJSON(t, env.router, http.MethodPost, "/auth/session",
		map[string]string{"address": env.adminAddr.Hex(), "signature": sig}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("session status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp dto.AuthSession
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" || resp.Role != "admin" || resp.ExpiresAt == 0 || resp.Address != env.adminAddr.Hex() {
		t.Fatalf("bad session response: %+v", resp)
	}

	// The JWT authorizes an admin-only route.
	got := doJSON(t, env.router, http.MethodGet, "/api/v1/compliance/wallets", nil,
		map[string]string{"Authorization": "Bearer " + resp.Token})
	if got.Code == http.StatusForbidden || got.Code == http.StatusUnauthorized {
		t.Fatalf("JWT admin route status = %d, want authorized", got.Code)
	}

	// Replaying the same (now-used) challenge signature must fail: single-use.
	replay := doJSON(t, env.router, http.MethodPost, "/auth/session",
		map[string]string{"address": env.adminAddr.Hex(), "signature": sig}, nil)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want 401 (single-use challenge)", replay.Code)
	}
}

// TestAdminAuthRejectsNonAdminSigner is the authorization gate: a wallet that
// controls a DIFFERENT address than the configured admin gets a valid-signature
// challenge but is refused a JWT.
func TestAdminAuthRejectsNonAdminSigner(t *testing.T) {
	env := setupTestApp(t)
	otherKey, _ := crypto.GenerateKey()
	otherAddr := crypto.PubkeyToAddress(otherKey.PublicKey)

	msg := requestChallenge(t, env, otherAddr.Hex())
	sig := personalSign(t, msg, otherKey)

	w := doJSON(t, env.router, http.MethodPost, "/auth/session",
		map[string]string{"address": otherAddr.Hex(), "signature": sig}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("non-admin signer status = %d, want 401", w.Code)
	}
}

// TestAdminAuthRejectsWrongSignature is the ownership gate: a signature that
// does not recover to the challenge's address is refused (and, per Verify's
// ordering, does not consume the pending challenge).
func TestAdminAuthRejectsWrongSignature(t *testing.T) {
	env := setupTestApp(t)
	wrongKey, _ := crypto.GenerateKey()

	msg := requestChallenge(t, env, env.adminAddr.Hex())
	// Sign the admin's challenge with the WRONG key.
	sig := personalSign(t, msg, wrongKey)

	w := doJSON(t, env.router, http.MethodPost, "/auth/session",
		map[string]string{"address": env.adminAddr.Hex(), "signature": sig}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-signature status = %d, want 401", w.Code)
	}

	// The mismatched attempt must NOT have burned the challenge — the real
	// admin can still complete login with it.
	good := personalSign(t, msg, env.adminKey)
	ok := doJSON(t, env.router, http.MethodPost, "/auth/session",
		map[string]string{"address": env.adminAddr.Hex(), "signature": good}, nil)
	if ok.Code != http.StatusOK {
		t.Fatalf("admin login after a wrong-signature attempt status = %d, want 200 (challenge must survive)", ok.Code)
	}
}

// TestAuditAttributesJWTAuthenticatedActionCorrectly covers the JWT path: a
// privileged action authenticated via the admin JWT must be attributed to the
// admin wallet address in the audit log, never "anonymous".
func TestAuditAttributesJWTAuthenticatedActionCorrectly(t *testing.T) {
	env := setupTestApp(t)

	freshProfile := `{
  "profileVersion": "1.0",
  "projectId": "11111111-2222-3333-4444-555555555555",
  "assetType": "allocated-gold-bar",
  "tokenUnit": "gram",
  "tokenDecimals": 18,
  "recordIdLabel": "Bar serial number",
  "assetSchema": {"type": "object", "additionalProperties": true}
}`
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/profile", json.RawMessage(freshProfile),
		map[string]string{"Authorization": env.bearer, "Idempotency-Key": "jwt-create-profile-1"})
	if w.Code != http.StatusCreated {
		t.Fatalf("createProfile status = %d, body=%s", w.Code, w.Body.String())
	}

	entries, err := env.app.Repos.AuditLogs.List(context.Background(), "assets", 10)
	if err != nil {
		t.Fatal(err)
	}
	var found *models.AuditLogEntry
	for _, e := range entries {
		if e.Action == "assets.createProfile" {
			found = e
		}
	}
	if found == nil {
		t.Fatal("expected an assets.createProfile audit entry")
	}
	if found.Actor != env.adminAddr.Hex() {
		t.Fatalf("Actor = %q, want the admin address %q", found.Actor, env.adminAddr.Hex())
	}
}
