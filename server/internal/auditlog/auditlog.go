// Package auditlog appends operational audit records to the audit_logs
// collection. They record what the server did and on whose request, such as a
// compliance change or an asset record, and are separate from the on-chain
// event log.
package auditlog

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Logger appends audit entries
type Logger struct {
	repo repository.AuditLogRepository
}

// New constructs a Logger backed by repo
func New(repo repository.AuditLogRepository) *Logger {
	return &Logger{repo: repo}
}

// Record appends one audit entry under category, such as "compliance" or
// "assets", which is also the category filter of listAuditLogs. The error is
// returned so the caller decides whether a failed write blocks the operation.
// The API handlers log it and continue.
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
// optionally filtered by category (empty string means all categories)
func (l *Logger) Recent(ctx context.Context, category string, limit int) ([]*models.AuditLogEntry, error) {
	return l.repo.List(ctx, category, limit)
}

// cloneMeta copies metadata so tagging an entry does not modify the caller's map
func cloneMeta(metadata map[string]any) map[string]any {
	m := make(map[string]any, len(metadata)+2)
	for k, v := range metadata {
		m[k] = v
	}
	return m
}

// RecordIntent records actor and action before an external effect, returning its entry
// id. Callers must abort on error; otherwise call RecordResult after the effect.
// phase="intent" without a matching result marks a possibly attempted effect with no
// recorded outcome.
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

// RecordResult completes RecordIntent with phase="result", intentId, ok and
// caller-supplied outcome metadata such as txHash or error. Return write failures for
// logging; callers need not fail the operation because its invocation is already
// recorded.
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
