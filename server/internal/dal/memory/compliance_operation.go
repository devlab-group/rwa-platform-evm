package memory

import (
	"context"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
)

// --- compliance operations ---

type ComplianceOperationRepository struct {
	mu sync.RWMutex
	l  []*models.ComplianceOperation
}

func NewComplianceOperationRepository() *ComplianceOperationRepository {
	return &ComplianceOperationRepository{}
}

func (r *ComplianceOperationRepository) Create(ctx context.Context, op *models.ComplianceOperation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *op
	r.l = append(r.l, &cp)
	return nil
}

func (r *ComplianceOperationRepository) List(ctx context.Context) ([]*models.ComplianceOperation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.ComplianceOperation, len(r.l))
	copy(out, r.l)
	return out, nil
}
