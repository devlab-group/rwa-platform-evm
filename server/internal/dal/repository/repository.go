// Package repository defines persistence interfaces for workflows and APIs, allowing
// tests to substitute internal/dal/memory for MongoDB
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
)

// ErrNotFound is returned when a lookup by ID finds nothing
var ErrNotFound = errors.New("repository: not found")

// ErrAlreadyExists is returned by Create when the identity already exists
var ErrAlreadyExists = errors.New("repository: already exists")

// ErrFencingTokenMismatch rejects missing or superseded tokens in
// IdempotencyRepository.Complete/Release and NonceLeaseRepository.Release. The caller
// must stop rather than retry the stale write.
var ErrFencingTokenMismatch = errors.New("repository: idempotency fencing token mismatch")

// ProjectRepository stores the single wallet-broadcast deployment observed by
// project.Service.ReconcileDeployment. MongoDB's fixed _id enforces one project
// document; the server reserves nothing before deployment broadcast.
type ProjectRepository interface {
	Get(ctx context.Context) (*models.Project, error)
	Upsert(ctx context.Context, p *models.Project) error
}

// AssetProfileRepository fixes a profile per deployment. Request handlers use Create,
// returning ErrAlreadyExists for an occupied projectId; validation has no persistence
// effects. Upsert replaces unconditionally and is outside request handling.
type AssetProfileRepository interface {
	Get(ctx context.Context, projectID string) (*models.AssetProfile, error)
	// GetCurrent selects the latest CreatedAt profile without requiring projectId, or
	// ErrNotFound if none exists
	GetCurrent(ctx context.Context) (*models.AssetProfile, error)
	Create(ctx context.Context, p *models.AssetProfile) error
	Upsert(ctx context.Context, p *models.AssetProfile) error
}

// AssetRecordRepository persists tokenization records
type AssetRecordRepository interface {
	List(ctx context.Context) ([]*models.AssetRecord, error)
	Get(ctx context.Context, recordID string) (*models.AssetRecord, error)
	Create(ctx context.Context, r *models.AssetRecord) error
	Update(ctx context.Context, r *models.AssetRecord) error
	// UpdateConditional requires Version==expectedVersion and increments it on success.
	// Return (false, nil) for a lost version race, requiring a re-read, or ErrNotFound for
	// a missing record. See TransactionRepository.UpdateConditional.
	UpdateConditional(ctx context.Context, r *models.AssetRecord, expectedVersion int) (bool, error)
	// ListPage uses ascending CreatedAt keysets; cursor="" starts, and nextCursor="" ends
	// the walk. See PurchaseRepository.ListPage.
	ListPage(ctx context.Context, cursor string, limit int) (items []*models.AssetRecord, nextCursor string, err error)
}

// AuditPackageRepository persists .rwa package build metadata
type AuditPackageRepository interface {
	Get(ctx context.Context, recordID string) (*models.AuditPackage, error)
	Upsert(ctx context.Context, p *models.AuditPackage) error
}

// AttestationRepository persists verified signed-results
type AttestationRepository interface {
	Get(ctx context.Context, recordID string) (*models.Attestation, error)
	Upsert(ctx context.Context, a *models.Attestation) error
}

// InvestorRepository persists wallet compliance status
type InvestorRepository interface {
	Get(ctx context.Context, address string) (*models.Investor, error)
	List(ctx context.Context) ([]*models.Investor, error)
	Upsert(ctx context.Context, inv *models.Investor) error
	// ListPage orders by unique address ascending, using the last address as cursor. Empty
	// cursor starts; empty nextCursor ends.
	ListPage(ctx context.Context, cursor string, limit int) (items []*models.Investor, nextCursor string, err error)
}

// WalletChallengeRepository persists one-time wallet-ownership challenges
type WalletChallengeRepository interface {
	Create(ctx context.Context, c *models.WalletChallenge) error
	Get(ctx context.Context, id string) (*models.WalletChallenge, error)
	// MarkUsed atomically changes Used=false to true. Return ErrAlreadyExists for a
	// consumed challenge or ErrNotFound for missing id, allowing one successful
	// consumption.
	MarkUsed(ctx context.Context, id string) error
	// CountActive counts unused challenges unexpired at now, matching the stored EIP-55
	// address exactly. ChallengeService.Create checks this before insertion.
	CountActive(ctx context.Context, address string, now time.Time) (int, error)
}

// KYCEventRepository stores webhook decisions before relay. Create atomically rejects
// duplicate payload hashes or (Provider, EventID) deliveries with ErrAlreadyExists,
// covering byte-identical and reserialized replays.
type KYCEventRepository interface {
	// Exists reports whether an event with this payload hash was already processed
	Exists(ctx context.Context, payloadHash string) (bool, error)
	Create(ctx context.Context, e *models.KYCEvent) error
	// Update replaces a stored event, to record a change of ApplyStatus or TxID
	Update(ctx context.Context, e *models.KYCEvent) error
	List(ctx context.Context) ([]*models.KYCEvent, error)
	// ListPending returns Claiming, Accepted and Applying, including interrupted claim
	// finalization for reconciler recovery
	ListPending(ctx context.Context) ([]*models.KYCEvent, error)
	// ListAppliedWithTx returns the Applied events that have a transaction id, the
	// only ones a deep reorg can reopen (WebhookReconciler.reopenReorgedApplied)
	ListAppliedWithTx(ctx context.Context) ([]*models.KYCEvent, error)
	// ClaimLatestForAddress atomically records a greater (occurredAt, eventKey) or an
	// initial claim. Later time wins, then lexicographically greater key. Return true on
	// acquisition, false for equal/newer existing claims; finalizeClaim chooses Accepted
	// or Recorded according to outcome.
	ClaimLatestForAddress(ctx context.Context, address string, occurredAt time.Time, eventKey string) (bool, error)
	// CurrentClaimEventKey returns the address's claim key or ErrNotFound. The reconciler
	// compares it to detect superseded decisions before relay.
	CurrentClaimEventKey(ctx context.Context, address string) (string, error)
}

// ComplianceOperationRepository persists submitted compliance status transactions
type ComplianceOperationRepository interface {
	Create(ctx context.Context, op *models.ComplianceOperation) error
	List(ctx context.Context) ([]*models.ComplianceOperation, error)
}

// KYCVerificationRepository binds provider references to wallets for webhook
// resolution, including Onfido's workflow-run references; see models.KYCVerification
type KYCVerificationRepository interface {
	// Upsert replaces an existing verification binding and derives v.ID from Provider/Ref,
	// allowing restarted verification
	Upsert(ctx context.Context, v *models.KYCVerification) error
	// GetByRef returns the binding for a provider reference, or ErrNotFound
	GetByRef(ctx context.Context, provider, ref string) (*models.KYCVerification, error)
}

// TransactionRepository persists transaction-manager state
type TransactionRepository interface {
	Create(ctx context.Context, tx *models.Transaction) error
	Get(ctx context.Context, id string) (*models.Transaction, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*models.Transaction, error)
	// GetByTxHash finds a chainID/hash match or ErrNotFound, preferring manager-submitted
	// records over event-derived ones so txindex avoids duplicate history entries
	GetByTxHash(ctx context.Context, chainID int64, txHash string) (*models.Transaction, error)
	// Update unconditionally replaces tx; callers must coordinate concurrent access, as
	// Submit does with signer locking and optional lease renewal. Use UpdateConditional
	// where writers can race.
	Update(ctx context.Context, tx *models.Transaction) error
	// UpdateConditional atomically requires Version==expectedVersion and increments it.
	// Return (false, nil) for a stale version, requiring a re-read, or ErrNotFound for
	// missing id.
	UpdateConditional(ctx context.Context, tx *models.Transaction, expectedVersion int) (bool, error)
	List(ctx context.Context) ([]*models.Transaction, error)
	// ListByStatuses returns every transaction whose Status is one of statuses, and
	// none when statuses is empty
	ListByStatuses(ctx context.Context, statuses ...models.TxStatus) ([]*models.Transaction, error)
	// ListUnfinalized excludes Replaced and Failed, and includes Confirmed only at
	// blockNumber >= confirmedFromBlock. Other statuses remain candidates; callers still
	// check finality before work.
	ListUnfinalized(ctx context.Context, confirmedFromBlock uint64) ([]*models.Transaction, error)
	// ListReplacements returns every record that replaces another (Replaces is not
	// empty), the work queue for reconcileReplacements
	ListReplacements(ctx context.Context) ([]*models.Transaction, error)
	// ListPage uses ascending SubmittedAt keysets with optional case-insensitive From/To
	// matching. Empty cursor starts; empty nextCursor ends.
	ListPage(ctx context.Context, address, cursor string, limit int) (items []*models.Transaction, nextCursor string, err error)
}

// ChainEventRepository persists indexed events, keyed by
// (chainId, address, txHash, logIndex)
type ChainEventRepository interface {
	Exists(ctx context.Context, key models.EventKey) (bool, error)
	Create(ctx context.Context, e *models.ChainEvent) error
	// DeleteFromBlock removes and counts events at or above fromBlock for (chainID,
	// address), supporting reorg rollback
	DeleteFromBlock(ctx context.Context, chainID int64, address string, fromBlock uint64) (int, error)
	ListByName(ctx context.Context, chainID int64, address, name string) ([]*models.ChainEvent, error)
	// ListAll returns unbounded chainID history across addresses, including Removed rows,
	// for read-model/projector rebuilds rather than paginated API reads
	ListAll(ctx context.Context, chainID int64) ([]*models.ChainEvent, error)
}

// PurchaseRepository persists Vault.buy fills
type PurchaseRepository interface {
	Create(ctx context.Context, p *models.Purchase) error
	List(ctx context.Context) ([]*models.Purchase, error)
	// DeleteAll is for stopped-server maintenance such as cmd/reindex. Live sales
	// reconciliation uses Upsert then DeleteStaleGeneration to avoid exposing empty or
	// partial replacement histories.
	DeleteAll(ctx context.Context) error
	// Upsert creates or replaces p.ID in place, keeping surviving IDs readable during
	// generation rebuilds
	Upsert(ctx context.Context, p *models.Purchase) error
	// DeleteStaleGeneration removes rows whose Generation differs from gen, after all
	// survivors are upserted with gen; see sales.Service.Reconcile
	DeleteStaleGeneration(ctx context.Context, gen int64) error
	// ListPage queries newest block first with keyset sorting and limit. Empty or
	// malformed cursor starts at the first page; return nextCursor from the last item, or
	// "" when finished.
	ListPage(ctx context.Context, cursor string, limit int) (items []*models.Purchase, nextCursor string, err error)
}

// RedemptionRequestRepository persists the redemption read model. Status must
// only be set from reconstructed chain events.
type RedemptionRequestRepository interface {
	// Upsert creates or replaces r.ID; redemption.Service.Reconcile stamps the rebuild
	// Generation first
	Upsert(ctx context.Context, r *models.RedemptionRequest) error
	Get(ctx context.Context, id string) (*models.RedemptionRequest, error)
	List(ctx context.Context, status string) ([]*models.RedemptionRequest, error)
	// DeleteAll removes every entry, for maintenance run with the server stopped,
	// such as cmd/reindex. See PurchaseRepository.DeleteAll.
	DeleteAll(ctx context.Context) error
	// DeleteStaleGeneration removes every record whose Generation is not gen. See
	// PurchaseRepository.DeleteStaleGeneration.
	DeleteStaleGeneration(ctx context.Context, gen int64) error
	// ListPage follows PurchaseRepository pagination with newest CreatedAt first, optional
	// exact status, and case-insensitive beneficiary address filtering
	ListPage(ctx context.Context, status, address, cursor string, limit int) (items []*models.RedemptionRequest, nextCursor string, err error)
}

// WalletSessionRepository stores auth.SessionManager's token digests. MongoDB shares
// validation and Delete revocation across server replicas, with TTL cleanup;
// PERSISTENCE_MODE=memory is process-local.
type WalletSessionRepository interface {
	Create(ctx context.Context, s *models.WalletSession) error
	// Get returns ErrNotFound for unknown or expired token IDs; responses must not
	// distinguish them. Enforce ExpiresAt directly rather than waiting for MongoDB TTL
	// cleanup; see auth.SessionManager.Validate.
	Get(ctx context.Context, token string) (*models.WalletSession, error)
	// Delete idempotently revokes the token ID, returning nil when unknown or expired
	Delete(ctx context.Context, token string) error
}

// AdminChallengeRepository stores auth.AdminChallengeService's address-keyed,
// single-use login challenges. MongoDB sharing lets any server replica verify a
// challenge; see models.AdminChallenge.
type AdminChallengeRepository interface {
	// Upsert replaces the address's active challenge, used or not, because verification
	// supplies no nonce to choose among messages
	Upsert(ctx context.Context, c *models.AdminChallenge) error
	// Get checks ExpiresAt directly, returning ErrNotFound for absent or expired
	// challenges rather than waiting for TTL cleanup
	Get(ctx context.Context, address string) (*models.AdminChallenge, error)
	// MarkUsed atomically consumes an unused address challenge. Return ErrAlreadyExists if
	// used or ErrNotFound if absent, allowing one successful consumption.
	MarkUsed(ctx context.Context, address string) error
}

// AuditLogRepository persists append-only operational audit entries
type AuditLogRepository interface {
	Append(ctx context.Context, e *models.AuditLogEntry) error
	// List returns the most recent entries, newest first, limited to category when
	// category is not empty
	List(ctx context.Context, category string, limit int) ([]*models.AuditLogEntry, error)
}

// IndexerCheckpointRepository persists scan progress for each (chainId, address)
type IndexerCheckpointRepository interface {
	Get(ctx context.Context, chainID int64, address string) (*models.IndexerCheckpoint, error)
	Set(ctx context.Context, c *models.IndexerCheckpoint) error
}

// BlockHashRepository stores bounded per-height hashes for deep-reorg ancestor
// searches. Record replaces a height's hash; PruneBefore enforces the indexer's
// retention boundary.
type BlockHashRepository interface {
	Record(ctx context.Context, chainID int64, blockNumber uint64, hash string) error
	// Get returns ErrNotFound for a height that was never recorded or has been pruned
	Get(ctx context.Context, chainID int64, blockNumber uint64) (string, error)
	PruneBefore(ctx context.Context, chainID int64, keepFrom uint64) error
}

// NonceLeaseRepository coordinates shared hot keys with holder/token matching, as
// IdempotencyRepository does. Acquire contends for ownership; Renew/Release require the
// winning pair. See models.NonceLease for key format.
type NonceLeaseRepository interface {
	// Acquire grants an absent or expired key to holderID for ttl, returning (token, true,
	// nil). Tokens start at 1 and strictly increase per key. A live lease, even this
	// holder's, returns (0, false, nil) unchanged; use Renew to extend it. Without
	// acquisition the caller must not proceed.
	Acquire(ctx context.Context, key, holderID string, ttl time.Duration) (token uint64, acquired bool, err error)
	// Renew extends expiry to now+ttl only for matching holderID/token. Return (false,
	// nil) after takeover; the old holder must abort persistence/broadcast, not retry
	// renewal.
	Renew(ctx context.Context, key, holderID string, token uint64, ttl time.Duration) (bool, error)
	// Release expires a matching holderID/token lease for immediate takeover. Otherwise
	// return ErrFencingTokenMismatch without changing it.
	Release(ctx context.Context, key, holderID string, token uint64) error
}

// BlockHashEntry is one (height, hash) pair recorded in a chunk commit
type BlockHashEntry struct {
	BlockNumber uint64
	Hash        string
}

// ChunkCommit groups events, retained block hashes, optional pruning and the advanced
// checkpoint. Apply atomically only at ExpectedCheckpointVersion.
type ChunkCommit struct {
	ChainID                   int64
	Events                    []*models.ChainEvent
	BlockHashes               []BlockHashEntry
	PruneBlockHashesBefore    *uint64
	Checkpoint                *models.IndexerCheckpoint
	ExpectedCheckpointVersion int
}

// ChunkRewind atomically deletes events from DeleteFromBlock across watched addresses
// and writes the rewound checkpoint at the expected version. This prevents a racing
// forward commit from leaving fork events stranded above the rewind.
type ChunkRewind struct {
	ChainID int64
	// Addresses are the contract addresses to delete events for
	Addresses []string
	// DeleteFromBlock is the lowest block number whose events are deleted
	DeleteFromBlock uint64
	// Checkpoint is the rewound checkpoint to store
	Checkpoint                *models.IndexerCheckpoint
	ExpectedCheckpointVersion int
}

// ChainChunkRepository commits scan chunks atomically with checkpoint-version checks.
// MongoDB uses majority-write transactions, requiring a replica set or sharded cluster.
type ChainChunkRepository interface {
	// CommitChunk atomically writes Events, BlockHashes, optional pruning and Checkpoint,
	// skipping duplicate events. Require the stored Version to equal
	// ExpectedCheckpointVersion, then store expected+1. Return (false, nil) for a race so
	// the caller discards the chunk and polls again; report invalid-checkpoint or storage
	// errors without partial writes.
	CommitChunk(ctx context.Context, c ChunkCommit) (committed bool, err error)

	// RewindChunk atomically deletes events from DeleteFromBlock for Addresses and stores
	// Checkpoint at ExpectedCheckpointVersion+1. Return (false, nil) on a version race,
	// requiring recomputation, or an invalid-checkpoint/storage error without partial
	// writes.
	RewindChunk(ctx context.Context, r ChunkRewind) (committed bool, err error)
}

// PublicationRepository persists models.PublicationRecord
type PublicationRepository interface {
	Get(ctx context.Context, id string) (*models.PublicationRecord, error)
	Upsert(ctx context.Context, r *models.PublicationRecord) error
	List(ctx context.Context) ([]*models.PublicationRecord, error)
}

// DeadLetterRepository stores indexer processing failures. Record upserts e.ID: repeats
// increment RetryCount, preserve FirstFailedAt, refresh LastFailedAt/ErrorMessage and
// clear Resolved, keeping recurring failures in one entry.
type DeadLetterRepository interface {
	Record(ctx context.Context, e *models.DeadLetterEntry) error
	Get(ctx context.Context, id string) (*models.DeadLetterEntry, error)
	List(ctx context.Context) ([]*models.DeadLetterEntry, error)
	// Resolve marks id Resolved, after a successful operator retry or a dismissal (see
	// models.DeadLetterEntry.Resolved)
	Resolve(ctx context.Context, id string) error
}

// IdempotencyRepository stores pending reservations and completed write responses by
// scoped Idempotency-Key. Reserve atomically admits one owner while the reservation is
// live; Complete/Release replace unsafe read-then-save coordination.
type IdempotencyRepository interface {
	Get(ctx context.Context, key string) (*models.IdempotencyRecord, error)
	// Reserve atomically claims an absent/expired key with ResponseStatus 0 and a fresh
	// token, returning (nil, true, token, nil). The winner must run the handler, then
	// Complete or Release with that token.
	// A live record returns (existing, false, "", nil); the caller must not run the
	// handler. Expiry permits takeover. Stale Complete/Release tokens return
	// ErrFencingTokenMismatch. Use an atomic insert/conditional upsert, never a separate
	// read then write.
	Reserve(ctx context.Context, key, method, path, requestHash string, ttl time.Duration) (existing *models.IdempotencyRecord, reserved bool, token string, err error)
	// Complete stores the response for key/token at most once per successful Reserve. A
	// missing or superseded reservation returns ErrFencingTokenMismatch without writing.
	Complete(ctx context.Context, key, token string, status int, body []byte) error
	// Release deletes key/token after a non-cacheable, non-2xx outcome so failures can be
	// retried. Cache only successful responses. Missing or changed tokens return
	// ErrFencingTokenMismatch without deletion.
	Release(ctx context.Context, key, token string) error
}

// Repositories bundles the repository for each collection, to be built once and
// passed to workflow and API constructors
type Repositories struct {
	Projects             ProjectRepository
	AssetProfiles        AssetProfileRepository
	AssetRecords         AssetRecordRepository
	AuditPackages        AuditPackageRepository
	Attestations         AttestationRepository
	Investors            InvestorRepository
	WalletChallenges     WalletChallengeRepository
	KYCEvents            KYCEventRepository
	KYCVerifications     KYCVerificationRepository
	ComplianceOperations ComplianceOperationRepository
	Transactions         TransactionRepository
	ChainEvents          ChainEventRepository
	Purchases            PurchaseRepository
	RedemptionRequests   RedemptionRequestRepository
	AuditLogs            AuditLogRepository
	IndexerCheckpoints   IndexerCheckpointRepository
	IndexerBlockHashes   BlockHashRepository
	IndexerChunks        ChainChunkRepository
	IndexerDeadLetters   DeadLetterRepository
	Idempotency          IdempotencyRepository
	Publications         PublicationRepository
	NonceLeases          NonceLeaseRepository
	WalletSessions       WalletSessionRepository
	AdminChallenges      AdminChallengeRepository
}
