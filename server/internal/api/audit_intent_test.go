package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/dal/models"
)

// failingAuditRepo fails Append at the chosen phase: failIntent before durable intent,
// or failResult after the effect
type failingAuditRepo struct {
	failIntent bool
	failResult bool
	intents    int
	results    int
}

func (r *failingAuditRepo) Append(ctx context.Context, e *models.AuditLogEntry) error {
	switch e.Metadata["phase"] {
	case "intent":
		r.intents++
		if r.failIntent {
			return errors.New("mongo unavailable (intent append)")
		}
	case "result":
		r.results++
		if r.failResult {
			return errors.New("mongo unavailable (result append)")
		}
	}
	return nil
}

func (r *failingAuditRepo) List(ctx context.Context, category string, limit int) ([]*models.AuditLogEntry, error) {
	return nil, nil
}

// TestSetComplianceStatusFailsClosedWhenAuditIntentUndurable checks that an
// intent-write failure returns 500 without broadcasting the compliance transaction
func TestSetComplianceStatusFailsClosedWhenAuditIntentUndurable(t *testing.T) {
	env := setupTestApp(t)
	repo := &failingAuditRepo{failIntent: true}
	env.app.Audit = auditlog.New(repo)

	before := len(env.client.SentTxs)
	body := SetStatusRequestBody{Address: addr("0xB0B4"), Status: "Allowed"}
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/status", body,
		map[string]string{"Authorization": env.bearer, "Idempotency-Key": "intent-fails"})

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (fail closed); body=%s", w.Code, w.Body.String())
	}
	if len(env.client.SentTxs) != before {
		t.Fatalf("a compliance tx must NOT be broadcast when the audit intent is undurable, got %d new sends", len(env.client.SentTxs)-before)
	}
	if repo.intents != 1 {
		t.Fatalf("expected exactly one intent attempt, got %d", repo.intents)
	}
}

// TestSetComplianceStatusSucceedsWhenAuditResultFails checks that a result-write
// failure after durable intent and broadcast is logged without failing the request
func TestSetComplianceStatusSucceedsWhenAuditResultFails(t *testing.T) {
	env := setupTestApp(t)
	repo := &failingAuditRepo{failResult: true}
	env.app.Audit = auditlog.New(repo)

	before := len(env.client.SentTxs)
	body := SetStatusRequestBody{Address: addr("0xB0B5"), Status: "Allowed"}
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/status", body,
		map[string]string{"Authorization": env.bearer, "Idempotency-Key": "result-fails"})

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (result-write failure must not fail the request); body=%s", w.Code, w.Body.String())
	}
	if len(env.client.SentTxs) <= before {
		t.Fatal("the compliance tx should have broadcast once the intent was durable")
	}
	if repo.intents != 1 || repo.results != 1 {
		t.Fatalf("expected one intent and one (failed) result attempt, got intents=%d results=%d", repo.intents, repo.results)
	}
}

// TestReissueRecordFailsClosedWhenAuditIntentUndurable checks that an intent-write
// failure returns 500 without changing the record
func TestReissueRecordFailsClosedWhenAuditIntentUndurable(t *testing.T) {
	env := setupTestApp(t)
	env.app.Audit = auditlog.New(&failingAuditRepo{failIntent: true})

	ctx := context.Background()
	rec := &models.AssetRecord{
		RecordID: "M08-REC", Status: models.RecordStatusPending, Amount: "1", Nonce: "12345",
		ValidUntil: 9999999999, RecordKey: "0x" + repeatHex("11", 32), MetadataDigest: "0x" + repeatHex("22", 32),
		CID: "bafk-m08", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := env.app.Repos.AssetRecords.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}

	w := doJSON(t, env.router, http.MethodPost, "/api/v1/assets/records/M08-REC/reissue", nil,
		map[string]string{"Authorization": env.bearer, "Idempotency-Key": "m08-reissue-intent-fails"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (fail closed); body=%s", w.Code, w.Body.String())
	}

	after, err := env.app.Repos.AssetRecords.Get(ctx, "M08-REC")
	if err != nil {
		t.Fatal(err)
	}
	if after.Nonce != "12345" {
		t.Fatalf("record nonce was rolled to %s despite the audit intent failing; the privileged effect must not have happened", after.Nonce)
	}
}

func repeatHex(b string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += b
	}
	return out
}
