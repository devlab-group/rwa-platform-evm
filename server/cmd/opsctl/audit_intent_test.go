package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/dal/models"
)

// failingAuditRepo fails Append for a chosen phase, so a test can drive a
// durable-store fault before the intent (failIntent) or after the op while
// writing the result (failResult).
type failingAuditRepo struct {
	failIntent bool
	failResult bool
}

func (r *failingAuditRepo) Append(ctx context.Context, e *models.AuditLogEntry) error {
	switch e.Metadata["phase"] {
	case "intent":
		if r.failIntent {
			return errors.New("mongo unavailable (intent)")
		}
	case "result":
		if r.failResult {
			return errors.New("mongo unavailable (result)")
		}
	}
	return nil
}

func (r *failingAuditRepo) List(ctx context.Context, category string, limit int) ([]*models.AuditLogEntry, error) {
	return nil, nil
}

func opsCtxWithAudit(repo *failingAuditRepo, out *bytes.Buffer) opsCtx {
	return opsCtx{ctx: context.Background(), repos: newTestRepos(), audit: auditlog.New(repo), actor: "opsctl:test", out: out}
}

// TestAuditedFailsClosedWhenIntentUndurable covers the DB-failure-before-
// mutation case: if the audit intent cannot be persisted, op must NOT
// run and audited returns an error.
func TestAuditedFailsClosedWhenIntentUndurable(t *testing.T) {
	var out bytes.Buffer
	o := opsCtxWithAudit(&failingAuditRepo{failIntent: true}, &out)

	ran := false
	err := o.audited("test.action", "target", nil, func() error {
		ran = true
		return nil
	})
	if err == nil {
		t.Fatal("expected audited to fail closed when the intent cannot be made durable")
	}
	if ran {
		t.Fatal("op must NOT run when its audit intent could not be persisted")
	}
	if !strings.Contains(out.String(), "refusing to run") {
		t.Fatalf("expected a refusal message, got %q", out.String())
	}
}

// TestAuditedRunsOpWhenOnlyResultWriteFails covers the DB-failure-after-
// mutation case: once the intent is durable, a failure to write the RESULT is
// only a warning — op still runs and its outcome is returned.
func TestAuditedRunsOpWhenOnlyResultWriteFails(t *testing.T) {
	var out bytes.Buffer
	o := opsCtxWithAudit(&failingAuditRepo{failResult: true}, &out)

	ran := false
	err := o.audited("test.action", "target", nil, func() error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("a result-write failure must not fail the op: %v", err)
	}
	if !ran {
		t.Fatal("op should have run (its intent was durable)")
	}
	if !strings.Contains(out.String(), "audit result write failed") {
		t.Fatalf("expected a result-write warning, got %q", out.String())
	}
}
