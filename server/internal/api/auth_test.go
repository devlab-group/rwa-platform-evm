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
// hex-encoded with a 0x prefix, the shape a wallet submits to POST /auth/session
func personalSign(t *testing.T, msg string, key *ecdsa.PrivateKey) string {
	t.Helper()
	sig, err := crypto.Sign(accounts.TextHash([]byte(msg)), key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27 // wallet convention: V in {27,28}; RecoverSigner normalizes either way
	return "0x" + hex.EncodeToString(sig)
}

// requestChallenge returns the nonce and personal_sign message from POST /auth/challenge
func requestChallenge(t *testing.T, env *testEnv, address string) (nonce, message string) {
	t.Helper()
	w := doJSON(t, env.router, http.MethodPost, "/auth/challenge", map[string]string{"address": address}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("challenge status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Address   string `json:"address"`
		Nonce     string `json:"nonce"`
		Message   string `json:"message"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Message == "" || resp.Nonce == "" || resp.ExpiresAt == 0 || resp.Address == "" {
		t.Fatalf("bad challenge response: %+v", resp)
	}
	return resp.Nonce, resp.Message
}

// TestAdminAuthChallengeSessionFlow checks that signing and exchanging an admin
// challenge yields a JWT authorizing an admin-only route
func TestAdminAuthChallengeSessionFlow(t *testing.T) {
	env := setupTestApp(t)

	nonce, msg := requestChallenge(t, env, env.adminAddr.Hex())
	sig := personalSign(t, msg, env.adminKey)

	w := doJSON(t, env.router, http.MethodPost, "/auth/session",
		map[string]string{"address": env.adminAddr.Hex(), "nonce": nonce, "signature": sig}, nil)
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

	// The JWT authorizes an admin-only route
	got := doJSON(t, env.router, http.MethodGet, "/api/v1/compliance/wallets", nil,
		map[string]string{"Authorization": "Bearer " + resp.Token})
	if got.Code != http.StatusOK {
		t.Fatalf("JWT admin route status = %d, want 200; body=%s", got.Code, got.Body.String())
	}

	// Replaying the used challenge signature fails, because challenges are single-use
	replay := doJSON(t, env.router, http.MethodPost, "/auth/session",
		map[string]string{"address": env.adminAddr.Hex(), "nonce": nonce, "signature": sig}, nil)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want 401 (single-use challenge)", replay.Code)
	}
}

// TestAdminAuthRejectsNonAdminSigner checks that a wallet controlling an address
// other than the configured admin gets a challenge but no JWT
func TestAdminAuthRejectsNonAdminSigner(t *testing.T) {
	env := setupTestApp(t)
	otherKey, _ := crypto.GenerateKey()
	otherAddr := crypto.PubkeyToAddress(otherKey.PublicKey)

	nonce, msg := requestChallenge(t, env, otherAddr.Hex())
	sig := personalSign(t, msg, otherKey)

	w := doJSON(t, env.router, http.MethodPost, "/auth/session",
		map[string]string{"address": otherAddr.Hex(), "nonce": nonce, "signature": sig}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("non-admin signer status = %d, want 401", w.Code)
	}
}

// TestAdminAuthRejectsWrongSignature checks that a signature that does not
// recover to the challenge's address is refused and does not consume the pending
// challenge
func TestAdminAuthRejectsWrongSignature(t *testing.T) {
	env := setupTestApp(t)
	wrongKey, _ := crypto.GenerateKey()

	nonce, msg := requestChallenge(t, env, env.adminAddr.Hex())
	// Sign the admin's challenge with the wrong key
	sig := personalSign(t, msg, wrongKey)

	w := doJSON(t, env.router, http.MethodPost, "/auth/session",
		map[string]string{"address": env.adminAddr.Hex(), "nonce": nonce, "signature": sig}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-signature status = %d, want 401", w.Code)
	}

	// The mismatched attempt did not consume the challenge, so the real admin can
	// still complete login with it
	good := personalSign(t, msg, env.adminKey)
	ok := doJSON(t, env.router, http.MethodPost, "/auth/session",
		map[string]string{"address": env.adminAddr.Hex(), "nonce": nonce, "signature": good}, nil)
	if ok.Code != http.StatusOK {
		t.Fatalf("admin login after a wrong-signature attempt status = %d, want 200 (challenge must survive)", ok.Code)
	}
}

// TestAuditAttributesJWTAuthenticatedActionCorrectly checks that a privileged
// action authenticated with the admin JWT is attributed to the admin address in
// the audit log, not to "anonymous"
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

// TestAdminAuthSessionRequiresMatchingNonce checks address-bound, single-use nonces and coexisting
// challenges at /auth/session
func TestAdminAuthSessionRequiresMatchingNonce(t *testing.T) {
	env := setupTestApp(t)
	addr := env.adminAddr.Hex()
	session := func(body map[string]string) int {
		return doJSON(t, env.router, http.MethodPost, "/auth/session", body, nil).Code
	}

	n1, m1 := requestChallenge(t, env, addr)
	n2, m2 := requestChallenge(t, env, addr)
	sig1, sig2 := personalSign(t, m1, env.adminKey), personalSign(t, m2, env.adminKey)

	if c := session(map[string]string{"address": addr, "signature": sig1}); c != http.StatusBadRequest {
		t.Fatalf("missing nonce status = %d, want 400", c)
	}
	if c := session(map[string]string{"address": addr, "nonce": "unknown", "signature": sig1}); c != http.StatusUnauthorized {
		t.Fatalf("unknown nonce status = %d, want 401", c)
	}
	// Both outstanding challenges remain usable
	if c := session(map[string]string{"address": addr, "nonce": n1, "signature": sig1}); c != http.StatusOK {
		t.Fatalf("first outstanding challenge status = %d, want 200", c)
	}
	if c := session(map[string]string{"address": addr, "nonce": n2, "signature": sig2}); c != http.StatusOK {
		t.Fatalf("second outstanding challenge status = %d, want 200", c)
	}
	if c := session(map[string]string{"address": addr, "nonce": n1, "signature": sig1}); c != http.StatusUnauthorized {
		t.Fatalf("reused nonce status = %d, want 401", c)
	}

	// A nonce for another wallet cannot authenticate the admin
	otherKey, _ := crypto.GenerateKey()
	otherAddr := crypto.PubkeyToAddress(otherKey.PublicKey).Hex()
	n3, m3 := requestChallenge(t, env, otherAddr)
	if c := session(map[string]string{"address": addr, "nonce": n3, "signature": personalSign(t, m3, env.adminKey)}); c != http.StatusUnauthorized {
		t.Fatalf("other-address nonce status = %d, want 401", c)
	}
}

func TestCreateAuthChallenge_NotConfigured_Is501(t *testing.T) {
	env := setupTestApp(t)
	env.app.AdminChallenges = nil
	w := doJSON(t, env.router, http.MethodPost, "/auth/challenge", map[string]string{"address": env.adminAddr.Hex()}, nil)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501, body=%s", w.Code, w.Body.String())
	}
}

func TestCreateAuthChallenge_RejectsBadRequests(t *testing.T) {
	tests := []struct {
		name string
		body any
	}{
		{"malformed address", map[string]string{"address": "0x1234"}},
		{"not hex", map[string]string{"address": "not-an-address"}},
		{"empty address", map[string]string{"address": ""}},
		{"missing address", map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupTestApp(t)
			w := doJSON(t, env.router, http.MethodPost, "/auth/challenge", tt.body, nil)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
			}
		})
	}
}
