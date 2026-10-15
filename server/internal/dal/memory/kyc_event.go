package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- kyc events ---

type KYCEventRepository struct {
	mu        sync.RWMutex
	byID      map[string]*models.KYCEvent
	byHash    map[string]bool
	byEventID map[string]bool // key: provider + "|" + eventId

	// claims stores each address's latest (occurredAt, eventKey), updated by
	// ClaimLatestForAddress
	claims map[string]claimState
}

type claimState struct {
	occurredAt time.Time
	eventKey   string
}

func NewKYCEventRepository() *KYCEventRepository {
	return &KYCEventRepository{
		byID:      map[string]*models.KYCEvent{},
		byHash:    map[string]bool{},
		byEventID: map[string]bool{},
		claims:    map[string]claimState{},
	}
}

func kycEventIDKey(provider, eventID string) string { return provider + "|" + eventID }

func (r *KYCEventRepository) Exists(ctx context.Context, payloadHash string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byHash[payloadHash], nil
}

func (r *KYCEventRepository) Create(ctx context.Context, e *models.KYCEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byHash[e.PayloadHash] {
		return repository.ErrAlreadyExists
	}
	eventKey := kycEventIDKey(e.Provider, e.EventID)
	if r.byEventID[eventKey] {
		return repository.ErrAlreadyExists
	}
	cp := *e
	r.byID[e.ID] = &cp
	r.byHash[e.PayloadHash] = true
	r.byEventID[eventKey] = true
	return nil
}

func (r *KYCEventRepository) List(ctx context.Context) ([]*models.KYCEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.KYCEvent, 0, len(r.byID))
	for _, v := range r.byID {
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReceivedAt.Before(out[j].ReceivedAt) })
	return out, nil
}

func (r *KYCEventRepository) Update(ctx context.Context, e *models.KYCEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[e.ID]; !ok {
		return repository.ErrNotFound
	}
	cp := *e
	r.byID[e.ID] = &cp
	return nil
}

func (r *KYCEventRepository) ListAppliedWithTx(ctx context.Context) ([]*models.KYCEvent, error) {
	all, _ := r.List(ctx)
	out := make([]*models.KYCEvent, 0)
	for _, v := range all {
		if v.ApplyStatus == models.KYCApplyApplied && v.TxID != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

func (r *KYCEventRepository) ListPending(ctx context.Context) ([]*models.KYCEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.KYCEvent, 0)
	for _, v := range r.byID {
		// Include Claiming so the reconciler can finish interrupted claim finalization
		if v.ApplyStatus != models.KYCApplyClaiming && v.ApplyStatus != models.KYCApplyAccepted && v.ApplyStatus != models.KYCApplyApplying {
			continue
		}
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OccurredAt.Before(out[j].OccurredAt) })
	return out, nil
}

// ClaimLatestForAddress atomically compares and stores under one lock. Later occurredAt wins;
// lexicographically greater eventKey breaks ties. Increasing decisions may win successively.
func (r *KYCEventRepository) ClaimLatestForAddress(ctx context.Context, address string, occurredAt time.Time, eventKey string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.claims[address]
	if ok && !claimIsNewer(occurredAt, eventKey, cur.occurredAt, cur.eventKey) {
		return false, nil
	}
	r.claims[address] = claimState{occurredAt: occurredAt, eventKey: eventKey}
	return true, nil
}

func (r *KYCEventRepository) CurrentClaimEventKey(ctx context.Context, address string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cur, ok := r.claims[address]
	if !ok {
		return "", repository.ErrNotFound
	}
	return cur.eventKey, nil
}

// claimIsNewer orders by occurredAt, then eventKey lexicographically, matching the
// MongoDB comparison
func claimIsNewer(at time.Time, key string, curAt time.Time, curKey string) bool {
	if at.After(curAt) {
		return true
	}
	if at.Before(curAt) {
		return false
	}
	return key > curKey
}
