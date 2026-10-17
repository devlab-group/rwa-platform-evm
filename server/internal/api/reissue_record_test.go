package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/dal/models"
)

func seedReissueRecord(t *testing.T, env *testEnv, id string, status models.RecordStatus) {
	t.Helper()
	rec := &models.AssetRecord{
		RecordID: id, Status: status, Amount: "1", Nonce: "12345", ValidUntil: 1000,
		RecordKey: "0x" + repeatHex("11", 32), MetadataDigest: "0x" + repeatHex("22", 32),
		CID: "bafk-reissue", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := env.app.Repos.AssetRecords.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}

func TestReissueRecord_RequiresAdminJWT(t *testing.T) {
	env := setupTestApp(t)
	seedReissueRecord(t, env, "REC-AUTH", models.RecordStatusPending)

	for name, headers := range map[string]map[string]string{
		"no credentials":      {"Idempotency-Key": "k"},
		"garbage bearer":      {"Authorization": "Bearer not-a-jwt", "Idempotency-Key": "k"},
		"wallet session only": {"X-Wallet-Session": "abc", "Idempotency-Key": "k"},
	} {
		t.Run(name, func(t *testing.T) {
			w := doJSON(t, env.router, http.MethodPost, "/api/v1/assets/records/REC-AUTH/reissue", nil, headers)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
			}
		})
	}
	got, _ := env.app.Repos.AssetRecords.Get(context.Background(), "REC-AUTH")
	if got.Nonce != "12345" || got.ValidUntil != 1000 || got.Version != 0 {
		t.Errorf("an unauthorised reissue changed the record: nonce=%s validUntil=%d version=%d", got.Nonce, got.ValidUntil, got.Version)
	}
}

func TestReissueRecord_AdminRefreshesNonceAndExpiry(t *testing.T) {
	env := setupTestApp(t)
	seedReissueRecord(t, env, "REC-OK", models.RecordStatusPending)

	w := doJSON(t, env.router, http.MethodPost, "/api/v1/assets/records/REC-OK/reissue", nil,
		map[string]string{"Authorization": env.bearer, "Idempotency-Key": "reissue-ok"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp dto.AssetRecordResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RecordID != "REC-OK" || resp.Status != string(models.RecordStatusPending) {
		t.Errorf("response = %+v, want the same Pending record", resp)
	}
	if resp.Nonce == "" || resp.Nonce == "12345" {
		t.Errorf("response nonce = %q, want a fresh nonce", resp.Nonce)
	}
	if resp.ValidUntil <= time.Now().Unix() {
		t.Errorf("response validUntil = %d, want a future expiry", resp.ValidUntil)
	}
	stored, _ := env.app.Repos.AssetRecords.Get(context.Background(), "REC-OK")
	if stored.Nonce != resp.Nonce || stored.ValidUntil != resp.ValidUntil || stored.Version != 1 {
		t.Errorf("stored nonce/validUntil/version = %s/%d/%d, want the response values and version 1", stored.Nonce, stored.ValidUntil, stored.Version)
	}
}

func TestReissueRecord_RefusesNonPendingAndUnknown(t *testing.T) {
	env := setupTestApp(t)
	for _, st := range []models.RecordStatus{models.RecordStatusSigned, models.RecordStatusMinted, models.RecordStatusRejected} {
		seedReissueRecord(t, env, "REC-"+string(st), st)
	}
	hdr := func(k string) map[string]string {
		return map[string]string{"Authorization": env.bearer, "Idempotency-Key": k}
	}
	for _, st := range []models.RecordStatus{models.RecordStatusSigned, models.RecordStatusMinted, models.RecordStatusRejected} {
		w := doJSON(t, env.router, http.MethodPost, "/api/v1/assets/records/REC-"+string(st)+"/reissue", nil, hdr("k-"+string(st)))
		if w.Code != http.StatusConflict {
			t.Errorf("%s: status = %d, want 409; body=%s", st, w.Code, w.Body.String())
		}
		if got, _ := env.app.Repos.AssetRecords.Get(context.Background(), "REC-"+string(st)); got.Nonce != "12345" {
			t.Errorf("%s: nonce changed to %s", st, got.Nonce)
		}
	}
	if w := doJSON(t, env.router, http.MethodPost, "/api/v1/assets/records/NOPE/reissue", nil, hdr("k-nope")); w.Code != http.StatusNotFound {
		t.Errorf("unknown record: status = %d, want 404", w.Code)
	}
	if w := doJSON(t, env.router, http.MethodPost, "/api/v1/assets/records/REC-"+string(models.RecordStatusSigned)+"/reissue", nil,
		map[string]string{"Authorization": env.bearer}); w.Code != http.StatusBadRequest {
		t.Errorf("missing Idempotency-Key: status = %d, want 400", w.Code)
	}
}
