// Package auditlog appends operational audit records to the audit_logs
// collection — a record of what the server itself did (whose API call
// triggered a compliance change, a mint relay, a redemption action, etc.),
// distinct from the on-chain event log.
package auditlog

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Logger appends audit entries.
type Logger struct {
	repo repository.AuditLogRepository
}

// New constructs a Logger backed by repo.
func New(repo repository.AuditLogRepository) *Logger {
	return &Logger{repo: repo}
}

// Record appends one audit entry under category (e.g. "compliance",
// "assets", "sales", "redemptions", "project" — matches api
// Schemas.AuditLog.category and the listAuditLogs `category` filter).
// Errors are returned rather than swallowed so callers can decide whether a
// failed audit write should block the triggering operation (V1 default:
// log and continue, see api handlers).
func (l *Logger) Record(ctx context.Context, category, actor, action, target string, metadata map[string]any) error {
	return l.repo.Append(ctx, &models.AuditLogEntry{
		ID:        uuid.NewString(),
		Category:  category,
		Actor:     actor,
		Action:    action,
		Target:    target,
		Metadata:  metadata,
		CreatedAt: time.Now().UTC(),
	})
}

// Recent returns up to limit of the most recently appended entries,
// optionally filtered by category (empty string means all categories).
func (l *Logger) Recent(ctx context.Context, category string, limit int) ([]*models.AuditLogEntry, error) {
	return l.repo.List(ctx, category, limit)
}

// cloneMeta copies metadata so the intent/result tagging below never mutates
// the caller's map (which may be reused or inspected afterwards).
func cloneMeta(metadata map[string]any) map[string]any {
	m := make(map[string]any, len(metadata)+2)
	for k, v := range metadata {
		m[k] = v
	}
	return m
}

// RecordIntent appends a durable pre-effect audit INTENT and returns its id.
// The immutable operation intent is persisted durably BEFORE any external
// effect. A privileged caller records the intent, treats a
// non-nil error as FATAL (fail closed — the external effect must not happen if
// the actor/action cannot be made durable), performs the effect, then calls
// RecordResult with the returned id. The entry is tagged phase="intent" so an
// intent with no matching result is a visible signal that an effect may have
// been attempted but never confirmed durable.
func (l *Logger) RecordIntent(ctx context.Context, category, actor, action, target string, metadata map[string]any) (string, error) {
	id := uuid.NewString()
	m := cloneMeta(metadata)
	m["phase"] = "intent"
	if err := l.repo.Append(ctx, &models.AuditLogEntry{
		ID:        id,
		Category:  category,
		Actor:     actor,
		Action:    action,
		Target:    target,
		Metadata:  m,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		return "", err
	}
	return id, nil
}

// RecordResult appends the completion entry for a prior RecordIntent, linked
// by intentId and tagged phase="result" with ok plus any outcome fields
// (txHash, error) in metadata — so completion/txhash/failure/retries are
// linked back to that intent. A persistence failure here is returned so the
// caller can log/alert, but — unlike the intent — it does NOT need to fail the
// operation: the actor/action was already durably captured by the intent.
func (l *Logger) RecordResult(ctx context.Context, intentID, category, actor, action, target string, ok bool, metadata map[string]any) error {
	m := cloneMeta(metadata)
	m["phase"] = "result"
	m["intentId"] = intentID
	m["ok"] = ok
	return l.repo.Append(ctx, &models.AuditLogEntry{
		ID:        uuid.NewString(),
		Category:  category,
		Actor:     actor,
		Action:    action,
		Target:    target,
		Metadata:  m,
		CreatedAt: time.Now().UTC(),
	})
}
