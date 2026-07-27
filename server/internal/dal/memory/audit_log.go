package memory

import (
	"context"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
)

// --- audit logs ---

type AuditLogRepository struct {
	mu sync.RWMutex
	l  []*models.AuditLogEntry
}

func NewAuditLogRepository() *AuditLogRepository { return &AuditLogRepository{} }

func (r *AuditLogRepository) Append(ctx context.Context, e *models.AuditLogEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *e
	r.l = append(r.l, &cp)
	return nil
}

func (r *AuditLogRepository) List(ctx context.Context, category string, limit int) ([]*models.AuditLogEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// most recent first, optionally filtered by category
	filtered := make([]*models.AuditLogEntry, 0, len(r.l))
	for i := len(r.l) - 1; i >= 0; i-- {
		if category != "" && r.l[i].Category != category {
			continue
		}
		filtered = append(filtered, r.l[i])
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered, nil
}
