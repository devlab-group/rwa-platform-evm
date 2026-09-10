package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/models"
)

// TestGetMyWalletStatusRequiresSession pins the auth boundary:
// /api/v1/me/wallet-status is gated by auth.RequireWalletSession, not the
// admin JWT — no header at all must 401.
func TestGetMyWalletStatusRequiresSession(t *testing.T) {
	env := setupTestApp(t)
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/me/wallet-status", nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", w.Code, w.Body.String())
	}
}

// TestGetMyWalletStatusRejectsInvalidSessionToken pins that a garbage/
// unknown X-Wallet-Session value is rejected the same as a missing one,
// not treated as some other principal.
func TestGetMyWalletStatusRejectsInvalidSessionToken(t *testing.T) {
	env := setupTestApp(t)
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/me/wallet-status", nil, map[string]string{"X-Wallet-Session": "not-a-real-token"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", w.Code, w.Body.String())
	}
}

// TestGetMyWalletStatusReturnsOwnStatus is the core case: a
// valid session returns the WalletStatus for exactly the address bound to
// that session (seeded here as Allowed), and never requires — or accepts
// — an admin credential.
func TestGetMyWalletStatusReturnsOwnStatus(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	address := addr("0xF00D")

	if err := env.app.Repos.Investors.Upsert(ctx, &models.Investor{
		Address: address, Status: models.ComplianceAllowed, ValidUntil: 1900000000, OwnershipVerified: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	token, _, err := env.app.Sessions.Issue(ctx, address)
	if err != nil {
		t.Fatal(err)
	}

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/me/wallet-status", nil, map[string]string{"X-Wallet-Session": token})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got dto.WalletStatus
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Address != address || got.Status != "Allowed" || !got.OwnershipVerified {
		t.Errorf("got %+v", got)
	}
}

// TestGetMyWalletStatusUnknownForAddressWithNoRecord covers the (in
// practice unreachable via the real verifyChallenge->session flow, since
// VerifyOwnership always upserts an Investor row first) defensive fallback
// for a session whose address has no Investor record at all.
func TestGetMyWalletStatusUnknownForAddressWithNoRecord(t *testing.T) {
	env := setupTestApp(t)
	address := addr("0xFEED")
	token, _, err := env.app.Sessions.Issue(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/me/wallet-status", nil, map[string]string{"X-Wallet-Session": token})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got dto.WalletStatus
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "Unknown" {
		t.Errorf("Status = %q, want Unknown", got.Status)
	}
}

// TestIsAddressAllowedIsPublicAndDisclosesOnlyAllowed covers the other
// half: no credential of any kind — no bearer token, no X-Wallet-Session —
// and the response body
// contains ONLY the "allowed" field — never status/validUntil/ownership,
// which would let an anonymous caller enumerate a third party's compliance
// state.
func TestIsAddressAllowedIsPublicAndDisclosesOnlyAllowed(t *testing.T) {
	env := setupTestApp(t)
	account := common.HexToAddress("0x0000000000000000000000000000000000B0B0")

	registry := bindings.NewComplianceRegistry()
	data, err := registry.PackIsAllowed(account)
	if err != nil {
		t.Fatal(err)
	}
	ret, err := registry.ABI.Methods["isAllowed"].Outputs.Pack(true)
	if err != nil {
		t.Fatal(err)
	}
	env.client.CallResponses[common.Bytes2Hex(data)] = ret

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/compliance/allowed/"+account.Hex(), nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 {
		t.Fatalf("expected exactly one field in the response, got %v", raw)
	}
	allowed, ok := raw["allowed"].(bool)
	if !ok || !allowed {
		t.Errorf("allowed = %v (ok=%v), want true", raw["allowed"], ok)
	}
}

// TestIsAddressAllowedReflectsFalse exercises the other outcome so the
// handler isn't just always returning true.
func TestIsAddressAllowedReflectsFalse(t *testing.T) {
	env := setupTestApp(t)
	account := common.HexToAddress("0x0000000000000000000000000000000000BAD1")

	registry := bindings.NewComplianceRegistry()
	data, err := registry.PackIsAllowed(account)
	if err != nil {
		t.Fatal(err)
	}
	ret, err := registry.ABI.Methods["isAllowed"].Outputs.Pack(false)
	if err != nil {
		t.Fatal(err)
	}
	env.client.CallResponses[common.Bytes2Hex(data)] = ret

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/compliance/allowed/"+account.Hex(), nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got dto.AllowedResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Allowed {
		t.Error("expected allowed=false")
	}
}

func TestIsAddressAllowedRejectsBadAddress(t *testing.T) {
	env := setupTestApp(t)
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/compliance/allowed/not-an-address", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestGetMyWalletStatusReportsOwnFrozenAmount: a frozen holder needs to be
// able to tell an enforcement hold apart from a broken app, so their own
// frozen amount travels on the subject-scoped status. Another holder's does
// not: the aggregate map stays admin-only.
func TestGetMyWalletStatusReportsOwnFrozenAmount(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	mine := addr("0xF00D")
	someoneElse := addr("0xBEEF")

	for _, a := range []string{mine, someoneElse} {
		if err := env.app.Repos.Investors.Upsert(ctx, &models.Investor{
			Address: a, Status: models.ComplianceAllowed, OwnershipVerified: true,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	p, err := env.app.Repos.Projects.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Security = &models.SecurityState{FrozenBalances: map[string]string{
		// Lowercased on purpose: the projection checksums its keys, and the
		// lookup must not depend on the casing either side happens to use.
		strings.ToLower(mine): "1500000000000000000",
		someoneElse:           "42",
	}}
	if err := env.app.Repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}

	token, _, err := env.app.Sessions.Issue(ctx, mine)
	if err != nil {
		t.Fatal(err)
	}
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/me/wallet-status", nil, map[string]string{"X-Wallet-Session": token})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got dto.WalletStatus
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.FrozenTokens != "1500000000000000000" {
		t.Errorf("frozenTokens = %q, want the caller's own 1500000000000000000", got.FrozenTokens)
	}
	if body := w.Body.String(); strings.Contains(body, `"42"`) {
		t.Errorf("another holder's frozen amount leaked: %s", body)
	}
}

// An unfrozen wallet omits the field rather than reporting "0".
func TestGetMyWalletStatusOmitsFrozenWhenNoneHeld(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	address := addr("0xF00D")

	if err := env.app.Repos.Investors.Upsert(ctx, &models.Investor{
		Address: address, Status: models.ComplianceAllowed, OwnershipVerified: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	token, _, err := env.app.Sessions.Issue(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/me/wallet-status", nil, map[string]string{"X-Wallet-Session": token})
	if body := w.Body.String(); strings.Contains(body, "frozenTokens") {
		t.Errorf("unfrozen wallet should omit frozenTokens: %s", body)
	}
}
