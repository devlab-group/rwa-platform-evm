// Package memory provides in-memory implementations of every
// internal/dal/repository interface. It is used by unit tests (and MAY be used
// for local development without Mongo) so business logic never requires a
// live database to be exercised.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// newFencingToken generates a random per-reservation fencing/lease token —
// 16 bytes is ample entropy for a value whose
// only job is to distinguish "the caller that currently owns this
// reservation" from a stale/superseded one; it is never parsed or compared
// for anything beyond byte-for-byte equality.
func newFencingToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// New builds a fresh, empty repository.Repositories backed entirely by memory.
func New() *repository.Repositories {
	chainEvents := NewChainEventRepository()
	blockHashes := NewBlockHashRepository()
	checkpoints := NewIndexerCheckpointRepository()
	return &repository.Repositories{
		Projects:             NewProjectRepository(),
		AssetProfiles:        NewAssetProfileRepository(),
		AssetRecords:         NewAssetRecordRepository(),
		AuditPackages:        NewAuditPackageRepository(),
		Attestations:         NewAttestationRepository(),
		Investors:            NewInvestorRepository(),
		WalletChallenges:     NewWalletChallengeRepository(),
		KYCEvents:            NewKYCEventRepository(),
		KYCVerifications:     NewKYCVerificationRepository(),
		ComplianceOperations: NewComplianceOperationRepository(),
		Transactions:         NewTransactionRepository(),
		ChainEvents:          chainEvents,
		Purchases:            NewPurchaseRepository(),
		RedemptionRequests:   NewRedemptionRequestRepository(),
		AuditLogs:            NewAuditLogRepository(),
		IndexerCheckpoints:   checkpoints,
		IndexerBlockHashes:   blockHashes,
		IndexerChunks:        NewChainChunkRepository(chainEvents, blockHashes, checkpoints),
		IndexerDeadLetters:   NewDeadLetterRepository(),
		Publications:         NewPublicationRepository(),
		NonceLeases:          NewNonceLeaseRepository(),
		Idempotency:          NewIdempotencyRepository(),
		WalletSessions:       NewWalletSessionRepository(),
		AdminChallenges:      NewAdminChallengeRepository(),
	}
}

// --- projects ---

type ProjectRepository struct {
	mu sync.RWMutex
	p  *models.Project
}

func NewProjectRepository() *ProjectRepository { return &ProjectRepository{} }

func (r *ProjectRepository) Get(ctx context.Context) (*models.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.p == nil {
		return nil, repository.ErrNotFound
	}
	cp := *r.p
	return &cp, nil
}

func (r *ProjectRepository) Upsert(ctx context.Context, p *models.Project) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *p
	r.p = &cp
	return nil
}

// --- asset profiles ---

type AssetProfileRepository struct {
	mu sync.RWMutex
	m  map[string]*models.AssetProfile
}

func NewAssetProfileRepository() *AssetProfileRepository {
	return &AssetProfileRepository{m: map[string]*models.AssetProfile{}}
}

func (r *AssetProfileRepository) Get(ctx context.Context, projectID string) (*models.AssetProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[projectID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

// GetCurrent returns the most-recent (by CreatedAt) stored profile, or
// repository.ErrNotFound when none exists — the single-tenant "the profile"
// accessor backing GET /api/v1/profile.
func (r *AssetProfileRepository) GetCurrent(ctx context.Context) (*models.AssetProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var current *models.AssetProfile
	for _, v := range r.m {
		if current == nil || v.CreatedAt.After(current.CreatedAt) {
			current = v
		}
	}
	if current == nil {
		return nil, repository.ErrNotFound
	}
	cp := *current
	return &cp, nil
}

func (r *AssetProfileRepository) Upsert(ctx context.Context, p *models.AssetProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *p
	r.m[p.ProjectID] = &cp
	return nil
}

// Create is create-once/CAS: it refuses to overwrite an existing profile
// for the same projectId (see the interface doc comment).
func (r *AssetProfileRepository) Create(ctx context.Context, p *models.AssetProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[p.ProjectID]; ok {
		return repository.ErrAlreadyExists
	}
	cp := *p
	r.m[p.ProjectID] = &cp
	return nil
}

// --- asset records ---

type AssetRecordRepository struct {
	mu sync.RWMutex
	m  map[string]*models.AssetRecord
}

func NewAssetRecordRepository() *AssetRecordRepository {
	return &AssetRecordRepository{m: map[string]*models.AssetRecord{}}
}

func (r *AssetRecordRepository) List(ctx context.Context) ([]*models.AssetRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.AssetRecord, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ListPage returns one bounded page — see the interface doc
// comment. Ascending-CreatedAt-first, matching assetRecordRepo's mongodb
// sort.
func (r *AssetRecordRepository) ListPage(ctx context.Context, cursor string, limit int) ([]*models.AssetRecord, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sorted := make([]*models.AssetRecord, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		sorted = append(sorted, &cp)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].RecordID < sorted[j].RecordID
	})
	page, next := repository.KeysetPage(sorted,
		func(r *models.AssetRecord) int64 { return r.CreatedAt.UnixNano() },
		func(r *models.AssetRecord) string { return r.RecordID },
		cursor, limit, false)
	return page, next, nil
}

func (r *AssetRecordRepository) Get(ctx context.Context, recordID string) (*models.AssetRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[recordID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *AssetRecordRepository) Create(ctx context.Context, rec *models.AssetRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[rec.RecordID]; ok {
		return repository.ErrAlreadyExists
	}
	cp := *rec
	r.m[rec.RecordID] = &cp
	return nil
}

func (r *AssetRecordRepository) Update(ctx context.Context, rec *models.AssetRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[rec.RecordID]; !ok {
		return repository.ErrNotFound
	}
	cp := *rec
	r.m[rec.RecordID] = &cp
	return nil
}

// UpdateConditional is the storage-level CAS — see the interface
// doc comment. The mutex makes the version check and write atomic.
func (r *AssetRecordRepository) UpdateConditional(ctx context.Context, rec *models.AssetRecord, expectedVersion int) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.m[rec.RecordID]
	if !ok {
		return false, repository.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return false, nil
	}
	cp := *rec
	cp.Version = expectedVersion + 1
	r.m[rec.RecordID] = &cp
	return true, nil
}

// --- audit packages ---

type AuditPackageRepository struct {
	mu sync.RWMutex
	m  map[string]*models.AuditPackage
}

func NewAuditPackageRepository() *AuditPackageRepository {
	return &AuditPackageRepository{m: map[string]*models.AuditPackage{}}
}

func (r *AuditPackageRepository) Get(ctx context.Context, recordID string) (*models.AuditPackage, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[recordID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *AuditPackageRepository) Upsert(ctx context.Context, p *models.AuditPackage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *p
	r.m[p.RecordID] = &cp
	return nil
}

// --- attestations ---

type AttestationRepository struct {
	mu sync.RWMutex
	m  map[string]*models.Attestation
}

func NewAttestationRepository() *AttestationRepository {
	return &AttestationRepository{m: map[string]*models.Attestation{}}
}

func (r *AttestationRepository) Get(ctx context.Context, recordID string) (*models.Attestation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[recordID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *AttestationRepository) Upsert(ctx context.Context, a *models.Attestation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *a
	r.m[a.RecordID] = &cp
	return nil
}

// --- investors ---

type InvestorRepository struct {
	mu sync.RWMutex
	m  map[string]*models.Investor
}

func NewInvestorRepository() *InvestorRepository {
	return &InvestorRepository{m: map[string]*models.Investor{}}
}

func (r *InvestorRepository) Get(ctx context.Context, address string) (*models.Investor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[address]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *InvestorRepository) List(ctx context.Context) ([]*models.Investor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.Investor, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

// ListPage returns one bounded, ascending-address page. Unlike
// Purchase/RedemptionRequest's ListPage, Address IS the collection's
// unique key already, so the cursor is simply the previous
// page's last address — no separate KeysetCursor/tiebreak is needed (see
// mongodb.investorRepo.ListPage's identical reasoning).
func (r *InvestorRepository) ListPage(ctx context.Context, cursor string, limit int) ([]*models.Investor, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sorted := make([]*models.Investor, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		sorted = append(sorted, &cp)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Address < sorted[j].Address })

	start := 0
	if cursor != "" {
		start = len(sorted)
		for i, v := range sorted {
			if v.Address > cursor {
				start = i
				break
			}
		}
	}
	end := start + limit
	if limit <= 0 || end > len(sorted) {
		end = len(sorted)
	}
	page := sorted[start:end]
	next := ""
	if end < len(sorted) {
		next = page[len(page)-1].Address
	}
	return page, next, nil
}

func (r *InvestorRepository) Upsert(ctx context.Context, inv *models.Investor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *inv
	r.m[inv.Address] = &cp
	return nil
}

// --- wallet challenges ---

type WalletChallengeRepository struct {
	mu sync.RWMutex
	m  map[string]*models.WalletChallenge
}

func NewWalletChallengeRepository() *WalletChallengeRepository {
	return &WalletChallengeRepository{m: map[string]*models.WalletChallenge{}}
}

func (r *WalletChallengeRepository) Create(ctx context.Context, c *models.WalletChallenge) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[c.ID]; ok {
		return repository.ErrAlreadyExists
	}
	cp := *c
	r.m[c.ID] = &cp
	return nil
}

func (r *WalletChallengeRepository) Get(ctx context.Context, id string) (*models.WalletChallenge, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *WalletChallengeRepository) MarkUsed(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.m[id]
	if !ok {
		return repository.ErrNotFound
	}
	if v.Used {
		return repository.ErrAlreadyExists
	}
	v.Used = true
	return nil
}

// CountActive is the memory-backed half of the per-address active-
// challenge cap: a linear scan is fine here since the in-memory repository
// only ever backs unit tests / local dev, never a production deployment's
// actual traffic volume (Mongo's indexed CountDocuments handles that case —
// see internal/dal/mongodb/compliance.go).
func (r *WalletChallengeRepository) CountActive(ctx context.Context, address string, now time.Time) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, v := range r.m {
		if v.Address == address && !v.Used && v.ExpiresAt.After(now) {
			n++
		}
	}
	return n, nil
}

// --- kyc events ---

type KYCEventRepository struct {
	mu        sync.RWMutex
	byID      map[string]*models.KYCEvent
	byHash    map[string]bool
	byEventID map[string]bool // key: provider + "|" + eventId

	// claims tracks, per address, which event currently holds the
	// "latest decision" claim. It is the atomic
	// analogue of the removed LatestForAddress query: instead of racing
	// a read-then-write against concurrent webhook deliveries, callers
	// CAS into this map via ClaimLatestForAddress using the deterministic
	// (occurredAt, eventKey) ordering, the same
	// singleton-claim pattern used elsewhere.
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

func (r *KYCEventRepository) ListPending(ctx context.Context) ([]*models.KYCEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.KYCEvent, 0)
	for _, v := range r.byID {
		// Claiming is included so the reconciler picks up an event whose
		// claim was never resolved because Process died at the claim/finalize
		// boundary.
		if v.ApplyStatus != models.KYCApplyClaiming && v.ApplyStatus != models.KYCApplyAccepted && v.ApplyStatus != models.KYCApplyApplying {
			continue
		}
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OccurredAt.Before(out[j].OccurredAt) })
	return out, nil
}

// ClaimLatestForAddress atomically decides whether the event described by
// (occurredAt, eventKey) is the newest decision seen so far for address,
// using the deterministic tie-break (occurredAt first, eventKey as a
// stable secondary key so two events with an identical timestamp resolve
// the same way everywhere). It replaces
// the removed LatestForAddress-then-Create race: the caller never reads
// the current latest and separately writes: the compare-and-set happens
// under a single lock acquisition, so concurrent webhook deliveries for
// the same address cannot both believe they "won".
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

// claimIsNewer reports whether (at, key) strictly outranks (curAt, curKey)
// under the (occurredAt, eventKey) ordering: later occurredAt wins; on an
// exact tie, the lexicographically greater eventKey wins. This must match
// the Mongo implementation's comparison exactly so memory- and
// Mongo-backed deployments make identical claim decisions.
func claimIsNewer(at time.Time, key string, curAt time.Time, curKey string) bool {
	if at.After(curAt) {
		return true
	}
	if at.Before(curAt) {
		return false
	}
	return key > curKey
}

// --- kyc verifications ---

type KYCVerificationRepository struct {
	mu sync.RWMutex
	m  map[string]*models.KYCVerification
}

func NewKYCVerificationRepository() *KYCVerificationRepository {
	return &KYCVerificationRepository{m: map[string]*models.KYCVerification{}}
}

func (r *KYCVerificationRepository) Upsert(ctx context.Context, v *models.KYCVerification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *v
	cp.ID = models.KYCVerificationID(v.Provider, v.Ref)
	r.m[cp.ID] = &cp
	return nil
}

func (r *KYCVerificationRepository) GetByRef(ctx context.Context, provider, ref string) (*models.KYCVerification, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[models.KYCVerificationID(provider, ref)]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

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

// --- transactions ---

type TransactionRepository struct {
	mu sync.RWMutex
	m  map[string]*models.Transaction
}

func NewTransactionRepository() *TransactionRepository {
	return &TransactionRepository{m: map[string]*models.Transaction{}}
}

func (r *TransactionRepository) Create(ctx context.Context, tx *models.Transaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[tx.ID]; ok {
		return repository.ErrAlreadyExists
	}
	cp := *tx
	r.m[tx.ID] = &cp
	return nil
}

func (r *TransactionRepository) Get(ctx context.Context, id string) (*models.Transaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *TransactionRepository) GetByIdempotencyKey(ctx context.Context, key string) (*models.Transaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.m {
		if v.IdempotencyKey == key && key != "" {
			cp := *v
			return &cp, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *TransactionRepository) GetByTxHash(ctx context.Context, chainID int64, txHash string) (*models.Transaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if txHash == "" {
		return nil, repository.ErrNotFound
	}
	var match *models.Transaction
	for _, v := range r.m {
		if v.ChainID != chainID || v.TxHash != txHash {
			continue
		}
		// Prefer a manager-submitted (non-EventDerived) record when several
		// share the hash, so the projector's dedup triggers deterministically.
		if match == nil || (!models.IsEventDerived(v) && models.IsEventDerived(match)) {
			cp := *v
			match = &cp
		}
	}
	if match == nil {
		return nil, repository.ErrNotFound
	}
	return match, nil
}

func (r *TransactionRepository) Update(ctx context.Context, tx *models.Transaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[tx.ID]; !ok {
		return repository.ErrNotFound
	}
	cp := *tx
	r.m[tx.ID] = &cp
	return nil
}

func (r *TransactionRepository) UpdateConditional(ctx context.Context, tx *models.Transaction, expectedVersion int) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.m[tx.ID]
	if !ok {
		return false, repository.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return false, nil
	}
	cp := *tx
	cp.Version = expectedVersion + 1
	r.m[tx.ID] = &cp
	return true, nil
}

func (r *TransactionRepository) List(ctx context.Context) ([]*models.Transaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.Transaction, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt.Before(out[j].SubmittedAt) })
	return out, nil
}

func (r *TransactionRepository) ListByStatus(ctx context.Context, status models.TxStatus) ([]*models.Transaction, error) {
	all, _ := r.List(ctx)
	out := make([]*models.Transaction, 0)
	for _, v := range all {
		if v.Status == status {
			out = append(out, v)
		}
	}
	return out, nil
}

// ListPage returns one bounded page — see the interface doc
// comment. Ascending-SubmittedAt-first, matching transactionRepo's mongodb
// sort; address, when non-empty, is matched case-insensitively against
// From OR To before the keyset walk (mirrors what api.listTransactions
// used to do in the handler).
func (r *TransactionRepository) ListPage(ctx context.Context, address, cursor string, limit int) ([]*models.Transaction, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	addrLower := strings.ToLower(address)
	sorted := make([]*models.Transaction, 0, len(r.m))
	for _, v := range r.m {
		if addrLower != "" && strings.ToLower(v.From) != addrLower && strings.ToLower(v.To) != addrLower {
			continue
		}
		cp := *v
		sorted = append(sorted, &cp)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].SubmittedAt.Equal(sorted[j].SubmittedAt) {
			return sorted[i].SubmittedAt.Before(sorted[j].SubmittedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})
	page, next := repository.KeysetPage(sorted,
		func(tx *models.Transaction) int64 { return tx.SubmittedAt.UnixNano() },
		func(tx *models.Transaction) string { return tx.ID },
		cursor, limit, false)
	return page, next, nil
}

// --- chain events ---

type ChainEventRepository struct {
	mu sync.RWMutex
	m  map[models.EventKey]*models.ChainEvent
}

func NewChainEventRepository() *ChainEventRepository {
	return &ChainEventRepository{m: map[models.EventKey]*models.ChainEvent{}}
}

func (r *ChainEventRepository) Exists(ctx context.Context, key models.EventKey) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.m[key]
	return ok, nil
}

func (r *ChainEventRepository) Create(ctx context.Context, e *models.ChainEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := models.EventKey{ChainID: e.ChainID, Address: e.Address, TxHash: e.TxHash, LogIndex: e.LogIndex}
	cp := *e
	r.m[key] = &cp
	return nil
}

func (r *ChainEventRepository) DeleteFromBlock(ctx context.Context, chainID int64, address string, fromBlock uint64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for k, v := range r.m {
		if v.ChainID == chainID && v.Address == address && v.BlockNumber >= fromBlock {
			delete(r.m, k)
			n++
		}
	}
	return n, nil
}

func (r *ChainEventRepository) ListByName(ctx context.Context, chainID int64, address, name string) ([]*models.ChainEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.ChainEvent, 0)
	for _, v := range r.m {
		if v.ChainID == chainID && v.Address == address && v.Name == name {
			cp := *v
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BlockNumber != out[j].BlockNumber {
			return out[i].BlockNumber < out[j].BlockNumber
		}
		return out[i].LogIndex < out[j].LogIndex
	})
	return out, nil
}

func (r *ChainEventRepository) ListAll(ctx context.Context, chainID int64) ([]*models.ChainEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.ChainEvent, 0)
	for _, v := range r.m {
		if v.ChainID == chainID {
			cp := *v
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BlockNumber != out[j].BlockNumber {
			return out[i].BlockNumber < out[j].BlockNumber
		}
		if out[i].LogIndex != out[j].LogIndex {
			return out[i].LogIndex < out[j].LogIndex
		}
		return out[i].TxHash < out[j].TxHash
	})
	return out, nil
}

// --- purchases ---

type PurchaseRepository struct {
	mu sync.RWMutex
	l  []*models.Purchase
}

func NewPurchaseRepository() *PurchaseRepository { return &PurchaseRepository{} }

func (r *PurchaseRepository) Create(ctx context.Context, p *models.Purchase) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *p
	r.l = append(r.l, &cp)
	return nil
}

func (r *PurchaseRepository) List(ctx context.Context) ([]*models.Purchase, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.Purchase, len(r.l))
	copy(out, r.l)
	return out, nil
}

func (r *PurchaseRepository) DeleteAll(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.l = nil
	return nil
}

// Upsert is the reader-safe half of a generation-swap rebuild —
// see the interface doc comment.
func (r *PurchaseRepository) Upsert(ctx context.Context, p *models.Purchase) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *p
	for i, existing := range r.l {
		if existing.ID == p.ID {
			r.l[i] = &cp
			return nil
		}
	}
	r.l = append(r.l, &cp)
	return nil
}

// DeleteStaleGeneration is the cleanup half — see the interface
// doc comment.
func (r *PurchaseRepository) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := make([]*models.Purchase, 0, len(r.l))
	for _, p := range r.l {
		if p.Generation == gen {
			kept = append(kept, p)
		}
	}
	r.l = kept
	return nil
}

// ListPage returns one bounded page — see the interface doc
// comment. Newest-block-first, matching purchaseRepo's mongodb sort.
func (r *PurchaseRepository) ListPage(ctx context.Context, cursor string, limit int) ([]*models.Purchase, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sorted := make([]*models.Purchase, len(r.l))
	copy(sorted, r.l)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].BlockNumber != sorted[j].BlockNumber {
			return sorted[i].BlockNumber > sorted[j].BlockNumber
		}
		return sorted[i].ID > sorted[j].ID
	})
	page, next := repository.KeysetPage(sorted,
		func(p *models.Purchase) int64 { return int64(p.BlockNumber) },
		func(p *models.Purchase) string { return p.ID },
		cursor, limit, true)
	out := make([]*models.Purchase, len(page))
	for i, p := range page {
		cp := *p
		out[i] = &cp
	}
	return out, next, nil
}

// --- redemption requests ---

type RedemptionRequestRepository struct {
	mu sync.RWMutex
	m  map[string]*models.RedemptionRequest
}

func NewRedemptionRequestRepository() *RedemptionRequestRepository {
	return &RedemptionRequestRepository{m: map[string]*models.RedemptionRequest{}}
}

func (r *RedemptionRequestRepository) Upsert(ctx context.Context, req *models.RedemptionRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *req
	r.m[req.ID] = &cp
	return nil
}

func (r *RedemptionRequestRepository) Get(ctx context.Context, id string) (*models.RedemptionRequest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *RedemptionRequestRepository) List(ctx context.Context, status string) ([]*models.RedemptionRequest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.RedemptionRequest, 0, len(r.m))
	for _, v := range r.m {
		if status != "" && string(v.Status) != status {
			continue
		}
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

func (r *RedemptionRequestRepository) DeleteAll(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m = map[string]*models.RedemptionRequest{}
	return nil
}

// DeleteStaleGeneration is the cleanup half of a generation-swap
// rebuild — see the interface doc comment.
func (r *RedemptionRequestRepository) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, v := range r.m {
		if v.Generation != gen {
			delete(r.m, id)
		}
	}
	return nil
}

// ListPage returns one bounded page — see the interface doc
// comment. Newest-CreatedAt-first, matching redemptionRequestRepo's
// mongodb sort.
func (r *RedemptionRequestRepository) ListPage(ctx context.Context, status, address, cursor string, limit int) ([]*models.RedemptionRequest, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	addrLower := strings.ToLower(address)
	var sorted []*models.RedemptionRequest
	for _, v := range r.m {
		if status != "" && string(v.Status) != status {
			continue
		}
		if addrLower != "" && strings.ToLower(v.Beneficiary) != addrLower {
			continue
		}
		cp := *v
		sorted = append(sorted, &cp)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt != sorted[j].CreatedAt {
			return sorted[i].CreatedAt > sorted[j].CreatedAt
		}
		return sorted[i].ID > sorted[j].ID
	})
	page, next := repository.KeysetPage(sorted,
		func(r *models.RedemptionRequest) int64 { return r.CreatedAt },
		func(r *models.RedemptionRequest) string { return r.ID },
		cursor, limit, true)
	return page, next, nil
}

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

// --- indexer checkpoints ---

type IndexerCheckpointRepository struct {
	mu sync.RWMutex
	m  map[string]*models.IndexerCheckpoint
}

func NewIndexerCheckpointRepository() *IndexerCheckpointRepository {
	return &IndexerCheckpointRepository{m: map[string]*models.IndexerCheckpoint{}}
}

func checkpointKey(chainID int64, address string) string {
	return fmt.Sprintf("%d:%s", chainID, address)
}

func (r *IndexerCheckpointRepository) Get(ctx context.Context, chainID int64, address string) (*models.IndexerCheckpoint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[checkpointKey(chainID, address)]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *IndexerCheckpointRepository) Set(ctx context.Context, c *models.IndexerCheckpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *c
	r.m[checkpointKey(c.ChainID, c.Address)] = &cp
	return nil
}

// --- indexer block hashes ---

type BlockHashRepository struct {
	mu sync.RWMutex
	m  map[int64]map[uint64]string
}

func NewBlockHashRepository() *BlockHashRepository {
	return &BlockHashRepository{m: map[int64]map[uint64]string{}}
}

func (r *BlockHashRepository) Record(ctx context.Context, chainID int64, blockNumber uint64, hash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m[chainID] == nil {
		r.m[chainID] = map[uint64]string{}
	}
	r.m[chainID][blockNumber] = hash
	return nil
}

func (r *BlockHashRepository) Get(ctx context.Context, chainID int64, blockNumber uint64) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.m[chainID][blockNumber]
	if !ok {
		return "", repository.ErrNotFound
	}
	return h, nil
}

func (r *BlockHashRepository) PruneBefore(ctx context.Context, chainID int64, keepFrom uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for bn := range r.m[chainID] {
		if bn < keepFrom {
			delete(r.m[chainID], bn)
		}
	}
	return nil
}

// --- nonce leases ---

type NonceLeaseRepository struct {
	mu sync.Mutex
	m  map[string]*models.NonceLease
}

func NewNonceLeaseRepository() *NonceLeaseRepository {
	return &NonceLeaseRepository{m: map[string]*models.NonceLease{}}
}

// Acquire grants the lease IFF free or expired, regardless of who
// previously held it (see the interface doc comment). Deliberately never
// deletes an expired entry — see models.NonceLease's doc comment on why a
// reset Token would break the fencing guarantee.
func (r *NonceLeaseRepository) Acquire(ctx context.Context, key, holderID string, ttl time.Duration) (uint64, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	existing, ok := r.m[key]
	if ok && existing.ExpiresAt.After(now) {
		return 0, false, nil
	}
	token := uint64(1)
	if ok {
		token = existing.Token + 1
	}
	r.m[key] = &models.NonceLease{Key: key, HolderID: holderID, Token: token, ExpiresAt: now.Add(ttl), UpdatedAt: now}
	return token, true, nil
}

func (r *NonceLeaseRepository) Renew(ctx context.Context, key, holderID string, token uint64, ttl time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.m[key]
	if !ok || existing.HolderID != holderID || existing.Token != token {
		return false, nil
	}
	existing.ExpiresAt = time.Now().UTC().Add(ttl)
	existing.UpdatedAt = time.Now().UTC()
	return true, nil
}

// Release marks the record immediately expired (free for the next
// Acquire) rather than deleting it — deleting would let the NEXT Acquire's
// existing.Token+1 computation start back over from 0/1, resetting the
// fencing token exactly like an unwanted TTL-based auto-delete would (see
// models.NonceLease's doc comment): a network-delayed, stale write from
// THIS SAME holder's earlier (now-released) session could then collide
// with a later session that reused the same reset token — the classic
// fencing-token ABA problem. Keeping the record (with its token
// preserved) means every future Acquire for key still computes a value
// strictly greater than anything ever issued for it.
func (r *NonceLeaseRepository) Release(ctx context.Context, key, holderID string, token uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.m[key]
	if !ok || existing.HolderID != holderID || existing.Token != token {
		return repository.ErrFencingTokenMismatch
	}
	now := time.Now().UTC()
	existing.ExpiresAt = now
	existing.UpdatedAt = now
	return nil
}

// --- ipfs publications ---

type PublicationRepository struct {
	mu sync.RWMutex
	m  map[string]*models.PublicationRecord
}

func NewPublicationRepository() *PublicationRepository {
	return &PublicationRepository{m: map[string]*models.PublicationRecord{}}
}

func (r *PublicationRepository) Get(ctx context.Context, id string) (*models.PublicationRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	cp.Destinations = append([]models.DestinationStatus(nil), v.Destinations...)
	return &cp, nil
}

func (r *PublicationRepository) Upsert(ctx context.Context, rec *models.PublicationRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *rec
	cp.Destinations = append([]models.DestinationStatus(nil), rec.Destinations...)
	r.m[rec.ID] = &cp
	return nil
}

func (r *PublicationRepository) List(ctx context.Context) ([]*models.PublicationRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.PublicationRecord, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		cp.Destinations = append([]models.DestinationStatus(nil), v.Destinations...)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// --- indexer atomic chunk commit ---

// ChainChunkRepository commits a scanned chunk atomically over the in-memory
// event/block-hash/checkpoint repos. Its own mutex — held across the version
// check and every write — is the atomicity guarantee (the mongodb impl uses a
// real Mongo transaction instead). In atomic mode the indexer only ever
// mutates the checkpoint through here, so no other writer races the version
// check under this lock.
type ChainChunkRepository struct {
	mu          sync.Mutex
	events      *ChainEventRepository
	blockHashes *BlockHashRepository
	checkpoints *IndexerCheckpointRepository
}

func NewChainChunkRepository(events *ChainEventRepository, blockHashes *BlockHashRepository, checkpoints *IndexerCheckpointRepository) *ChainChunkRepository {
	return &ChainChunkRepository{events: events, blockHashes: blockHashes, checkpoints: checkpoints}
}

func (r *ChainChunkRepository) CommitChunk(ctx context.Context, c repository.ChunkCommit) (bool, error) {
	if c.Checkpoint == nil {
		return false, repository.ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	curVersion := 0
	if cur, err := r.checkpoints.Get(ctx, c.Checkpoint.ChainID, c.Checkpoint.Address); err == nil {
		curVersion = cur.Version
	} else if err != repository.ErrNotFound {
		return false, err
	}
	if curVersion != c.ExpectedCheckpointVersion {
		return false, nil // a concurrent writer advanced the checkpoint; caller lost the race
	}

	for _, e := range c.Events {
		if err := r.events.Create(ctx, e); err != nil {
			return false, err
		}
	}
	for _, bh := range c.BlockHashes {
		if err := r.blockHashes.Record(ctx, c.ChainID, bh.BlockNumber, bh.Hash); err != nil {
			return false, err
		}
	}
	if c.PruneBlockHashesBefore != nil {
		if err := r.blockHashes.PruneBefore(ctx, c.ChainID, *c.PruneBlockHashesBefore); err != nil {
			return false, err
		}
	}
	cp := *c.Checkpoint
	cp.Version = c.ExpectedCheckpointVersion + 1
	if err := r.checkpoints.Set(ctx, &cp); err != nil {
		return false, err
	}
	return true, nil
}

// RewindChunk applies a reorg rollback / trusted reset under the same lock and
// the same version fence CommitChunk uses — see
// repository.ChainChunkRepository.RewindChunk.
func (r *ChainChunkRepository) RewindChunk(ctx context.Context, rw repository.ChunkRewind) (bool, error) {
	if rw.Checkpoint == nil {
		return false, repository.ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	curVersion := 0
	if cur, err := r.checkpoints.Get(ctx, rw.Checkpoint.ChainID, rw.Checkpoint.Address); err == nil {
		curVersion = cur.Version
	} else if err != repository.ErrNotFound {
		return false, err
	}
	if curVersion != rw.ExpectedCheckpointVersion {
		return false, nil // a concurrent writer moved the checkpoint; recompute the rewind
	}

	for _, addr := range rw.Addresses {
		if _, err := r.events.DeleteFromBlock(ctx, rw.ChainID, addr, rw.DeleteFromBlock); err != nil {
			return false, err
		}
	}
	cp := *rw.Checkpoint
	cp.Version = rw.ExpectedCheckpointVersion + 1
	if err := r.checkpoints.Set(ctx, &cp); err != nil {
		return false, err
	}
	return true, nil
}

// --- indexer dead letters ---

type DeadLetterRepository struct {
	mu sync.RWMutex
	m  map[string]*models.DeadLetterEntry
}

func NewDeadLetterRepository() *DeadLetterRepository {
	return &DeadLetterRepository{m: map[string]*models.DeadLetterEntry{}}
}

func (r *DeadLetterRepository) Record(ctx context.Context, e *models.DeadLetterEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *e
	if existing, ok := r.m[e.ID]; ok {
		cp.FirstFailedAt = existing.FirstFailedAt
		cp.RetryCount = existing.RetryCount + 1
		cp.Resolved = false // a fresh failure un-resolves a previously-resolved entry
	}
	r.m[e.ID] = &cp
	return nil
}

func (r *DeadLetterRepository) Get(ctx context.Context, id string) (*models.DeadLetterEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *e
	return &cp, nil
}

func (r *DeadLetterRepository) List(ctx context.Context) ([]*models.DeadLetterEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.DeadLetterEntry, 0, len(r.m))
	for _, e := range r.m {
		cp := *e
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstFailedAt.Before(out[j].FirstFailedAt) })
	return out, nil
}

func (r *DeadLetterRepository) Resolve(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[id]
	if !ok {
		return repository.ErrNotFound
	}
	e.Resolved = true
	return nil
}

// --- idempotency ---

type IdempotencyRepository struct {
	mu sync.RWMutex
	m  map[string]*models.IdempotencyRecord
}

func NewIdempotencyRepository() *IdempotencyRepository {
	return &IdempotencyRepository{m: map[string]*models.IdempotencyRecord{}}
}

func (r *IdempotencyRepository) Get(ctx context.Context, key string) (*models.IdempotencyRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[key]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

// Reserve is atomic under r.mu: exactly one caller observes (nil, true,
// token, nil) for a given key at a time. An existing record whose
// ExpiresAt has already passed is treated as free rather than returned —
// this is the "expired-reservation takeover" behavior: without it, crashed
// pending reservations remain stuck forever because ExpiresAt is written but
// never enforced. A reservation nobody Complete()d/Release()d within ttl
// no longer blocks a fresh attempt, bounding how long a crash can wedge a
// key to ttl instead of forever. Because the whole check-then-overwrite
// happens under one lock, a second caller racing in immediately after
// never observes the stale expired record either — it sees the new
// caller's fresh pending entry and correctly gets (existing, false, "",
// nil).
//
// Every winning Reserve mints a fresh random token and
// stores it on the new record; Complete/Release below refuse to act
// unless the caller presents that exact token, so a caller whose
// reservation already got taken over by a later Reserve (it was slow
// enough for ttl to elapse) can no longer finish/cancel the NEW owner's
// reservation out from under it.
func (r *IdempotencyRepository) Reserve(ctx context.Context, key, method, path, requestHash string, ttl time.Duration) (*models.IdempotencyRecord, bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	if existing, ok := r.m[key]; ok && existing.ExpiresAt.After(now) {
		cp := *existing
		cp.Token = "" // never hand a live reservation's fencing token to a caller that didn't win it
		return &cp, false, "", nil
	}
	token, err := newFencingToken()
	if err != nil {
		return nil, false, "", err
	}
	r.m[key] = &models.IdempotencyRecord{
		Key: key, Method: method, Path: path, RequestHash: requestHash, Token: token,
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	return nil, true, token, nil
}

// Complete matches the existing record by key AND requires token to equal
// the record's current fencing token: a caller so slow that ttl elapsed and
// a second caller has already Reserve()d and possibly Complete()d the same
// key presents a now-stale token here and is rejected with
// ErrFencingTokenMismatch instead of silently overwriting the new owner's
// response.
func (r *IdempotencyRepository) Complete(ctx context.Context, key, token string, status int, body []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.m[key]
	if !ok || rec.Token != token {
		return repository.ErrFencingTokenMismatch
	}
	rec.ResponseStatus = status
	rec.ResponseBody = body
	return nil
}

// Release deletes the reservation for key only if token matches its
// current fencing token — same reasoning as Complete: a stale/superseded
// owner must not be able to delete a later owner's live reservation out
// from under it.
func (r *IdempotencyRepository) Release(ctx context.Context, key, token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.m[key]
	if !ok || rec.Token != token {
		return repository.ErrFencingTokenMismatch
	}
	delete(r.m, key)
	return nil
}

// --- wallet sessions ---

type WalletSessionRepository struct {
	mu sync.Mutex
	m  map[string]*models.WalletSession
}

func NewWalletSessionRepository() *WalletSessionRepository {
	return &WalletSessionRepository{m: map[string]*models.WalletSession{}}
}

func (r *WalletSessionRepository) Create(ctx context.Context, s *models.WalletSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	r.m[s.Token] = &cp
	return nil
}

func (r *WalletSessionRepository) Get(ctx context.Context, token string) (*models.WalletSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[token]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if time.Now().UTC().After(s.ExpiresAt) {
		delete(r.m, token) // opportunistic eviction, same pattern as the old in-process SessionManager
		return nil, repository.ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (r *WalletSessionRepository) Delete(ctx context.Context, token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, token)
	return nil
}

// --- admin challenges (single-use admin wallet login) ---

type AdminChallengeRepository struct {
	mu sync.Mutex
	m  map[string]*models.AdminChallenge
}

func NewAdminChallengeRepository() *AdminChallengeRepository {
	return &AdminChallengeRepository{m: map[string]*models.AdminChallenge{}}
}

func (r *AdminChallengeRepository) Upsert(ctx context.Context, c *models.AdminChallenge) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *c
	r.m[c.Address] = &cp
	return nil
}

func (r *AdminChallengeRepository) Get(ctx context.Context, address string) (*models.AdminChallenge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.m[address]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if time.Now().UTC().After(c.ExpiresAt) {
		delete(r.m, address) // opportunistic eviction, same pattern as the session repos
		return nil, repository.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *AdminChallengeRepository) MarkUsed(ctx context.Context, address string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.m[address]
	if !ok {
		return repository.ErrNotFound
	}
	if c.Used {
		return repository.ErrAlreadyExists
	}
	c.Used = true
	return nil
}
