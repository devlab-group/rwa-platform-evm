// Package repository defines the storage interfaces for the persisted collections.
// Business logic depends only on these interfaces, so tests can use the in-memory
// implementation in internal/dal/memory instead of a MongoDB server.
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

// ErrFencingTokenMismatch is returned by IdempotencyRepository.Complete and
// Release, and by NonceLeaseRepository.Release, when the caller's token does not
// match the stored one: the reservation or lease expired and was taken over, or
// was never held under that token. The caller must stop and must not retry the
// same write.
var ErrFencingTokenMismatch = errors.New("repository: idempotency fencing token mismatch")

// ProjectRepository persists the single project record. The server only observes
// a deployment that the admin broadcast from a wallet (project.Service.ReconcileDeployment
// folds the ProjectDeployed event into this record), so there is nothing to
// reserve before a broadcast. The MongoDB document has a fixed _id, which makes
// "one project per deployment" a database guarantee.
type ProjectRepository interface {
	Get(ctx context.Context) (*models.Project, error)
	Upsert(ctx context.Context, p *models.Project) error
}

// AssetProfileRepository persists the asset profile, which is fixed for a
// deployment. Create is create-once and returns ErrAlreadyExists for a projectId
// that already has a profile. Request handlers use Create, because profile
// validation must have no side effects. Upsert replaces a profile unconditionally
// and is not used by request handlers.
type AssetProfileRepository interface {
	Get(ctx context.Context, projectID string) (*models.AssetProfile, error)
	// GetCurrent returns the deployment's Asset Profile without a projectId. If more
	// than one exists it returns the one with the latest CreatedAt, and it returns
	// ErrNotFound when none exists.
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
	// UpdateConditional replaces r only if the stored record's Version equals
	// expectedVersion, and then increments Version. It returns (false, nil) on a
	// version mismatch, meaning the caller lost a race and must re-read the record
	// before deciding again, and ErrNotFound if the record does not exist. It mirrors
	// TransactionRepository.UpdateConditional.
	UpdateConditional(ctx context.Context, r *models.AssetRecord, expectedVersion int) (bool, error)
	// ListPage returns one page in ascending CreatedAt order using keyset
	// pagination (see PurchaseRepository.ListPage). cursor is "" for the first page,
	// and nextCursor is "" when there is no further page
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
	// ListPage returns one page in ascending address order. The cursor is the
	// previous page's last address, since Address is the unique key. cursor is "" for
	// the first page, and nextCursor is "" when there is no further page.
	ListPage(ctx context.Context, cursor string, limit int) (items []*models.Investor, nextCursor string, err error)
}

// WalletChallengeRepository persists one-time wallet-ownership challenges
type WalletChallengeRepository interface {
	Create(ctx context.Context, c *models.WalletChallenge) error
	Get(ctx context.Context, id string) (*models.WalletChallenge, error)
	// MarkUsed sets Used from false to true in one atomic conditional update, so two
	// concurrent verifications of the same challenge cannot both succeed. It returns
	// ErrAlreadyExists if the challenge was already used and ErrNotFound if id does
	// not exist.
	MarkUsed(ctx context.Context, id string) error
	// CountActive returns how many challenges for address are neither used nor expired
	// at now. address must be in the normalized form (EIP-55 checksum) the caller
	// stores, and the match is exact. ChallengeService.Create uses the count to cap
	// outstanding challenges per address.
	CountActive(ctx context.Context, address string, now time.Time) (int, error)
}

// KYCEventRepository persists the webhook inbox and outbox: a decision is stored
// before its on-chain status is applied. Create enforces two uniqueness
// constraints atomically, the payload hash (a byte-for-byte replay) and
// (Provider, EventID) (the same event serialized or signed differently), and
// returns ErrAlreadyExists on either.
type KYCEventRepository interface {
	// Exists reports whether an event with this payload hash was already processed
	Exists(ctx context.Context, payloadHash string) (bool, error)
	Create(ctx context.Context, e *models.KYCEvent) error
	// Update replaces a stored event, to record a change of ApplyStatus or TxID
	Update(ctx context.Context, e *models.KYCEvent) error
	List(ctx context.Context) ([]*models.KYCEvent, error)
	// ListPending returns the events whose ApplyStatus is Claiming, Accepted or
	// Applying, which the reconciler works through. Claiming is included so an event
	// stored before its claim was resolved is picked up again.
	ListPending(ctx context.Context) ([]*models.KYCEvent, error)
	// ListAppliedWithTx returns the Applied events that have a transaction id, the
	// only ones a deep reorg can reopen (WebhookReconciler.reopenReorgedApplied)
	ListAppliedWithTx(ctx context.Context) ([]*models.KYCEvent, error)
	// ClaimLatestForAddress records (occurredAt, eventKey) as the winning decision for
	// address if it is strictly newer than the current claim or there is none. Order
	// is by occurredAt, then eventKey lexicographically. It returns true when the
	// caller won and should store the event as Accepted, and false when an equal or
	// newer decision holds the claim, so the delivery is stale. The comparison and the
	// write are one atomic step.
	ClaimLatestForAddress(ctx context.Context, address string, occurredAt time.Time, eventKey string) (bool, error)
	// CurrentClaimEventKey returns the eventKey that holds the claim for address, or
	// ErrNotFound if there is none. The reconciler uses it to detect that an event was
	// superseded and must not be applied on-chain.
	CurrentClaimEventKey(ctx context.Context, address string) (string, error)
}

// ComplianceOperationRepository persists submitted compliance status transactions
type ComplianceOperationRepository interface {
	Create(ctx context.Context, op *models.ComplianceOperation) error
	List(ctx context.Context) ([]*models.ComplianceOperation, error)
}

// KYCVerificationRepository persists the binding from a provider reference to the
// wallet address a KYC verification was started for. It lets a webhook whose
// payload carries only a provider reference, as Onfido's does, be resolved to the
// wallet. See models.KYCVerification.
type KYCVerificationRepository interface {
	// Upsert stores the binding and overwrites an existing one, so restarting a
	// verification is not an error. It sets v.ID from Provider and Ref.
	Upsert(ctx context.Context, v *models.KYCVerification) error
	// GetByRef returns the binding for a provider reference, or ErrNotFound
	GetByRef(ctx context.Context, provider, ref string) (*models.KYCVerification, error)
}

// TransactionRepository persists transaction-manager state
type TransactionRepository interface {
	Create(ctx context.Context, tx *models.Transaction) error
	Get(ctx context.Context, id string) (*models.Transaction, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*models.Transaction, error)
	// GetByTxHash returns the record with this txHash on chainID, or ErrNotFound. The
	// event-derived transaction projector (txindex.ReconcileStackTransactions) uses it
	// to avoid listing a server-submitted transaction twice. When a submitted record
	// and an event-derived record share a hash, the submitted one is returned.
	GetByTxHash(ctx context.Context, chainID int64, txHash string) (*models.Transaction, error)
	// Update replaces tx unconditionally, for callers that already have exclusive
	// access, such as Submit under the per-signer lock and, in mongo-lease mode, a
	// just-renewed lease. Use UpdateConditional for a write that could race another
	// writer.
	Update(ctx context.Context, tx *models.Transaction) error
	// UpdateConditional replaces tx only if the stored record's Version equals
	// expectedVersion, and then increments Version, so a stale writer is rejected at
	// the write itself. It returns (false, nil) on a version mismatch, where the
	// caller must re-read and decide again, and ErrNotFound if id does not exist.
	UpdateConditional(ctx context.Context, tx *models.Transaction, expectedVersion int) (bool, error)
	List(ctx context.Context) ([]*models.Transaction, error)
	// ListByStatuses returns every transaction whose Status is one of statuses, and
	// none when statuses is empty
	ListByStatuses(ctx context.Context, statuses ...models.TxStatus) ([]*models.Transaction, error)
	// ListUnfinalized returns the transactions TxManager.RefreshStatuses still has work
	// for: every status except Replaced, Failed and Confirmed, plus Confirmed records
	// whose blockNumber is at least confirmedFromBlock, so confirmed transactions past
	// their reorg recheck window are not read again. Callers still apply their own
	// finality check to the result.
	ListUnfinalized(ctx context.Context, confirmedFromBlock uint64) ([]*models.Transaction, error)
	// ListReplacements returns every record that replaces another (Replaces is not
	// empty), the work queue for reconcileReplacements
	ListReplacements(ctx context.Context) ([]*models.Transaction, error)
	// ListPage returns one page in ascending SubmittedAt order using keyset
	// pagination. A non-empty address keeps only transactions whose From or To equals
	// it, ignoring case. cursor is "" for the first page, and nextCursor is "" when
	// there is no further page.
	ListPage(ctx context.Context, address, cursor string, limit int) (items []*models.Transaction, nextCursor string, err error)
}

// ChainEventRepository persists indexed events, keyed by
// (chainId, address, txHash, logIndex)
type ChainEventRepository interface {
	Exists(ctx context.Context, key models.EventKey) (bool, error)
	Create(ctx context.Context, e *models.ChainEvent) error
	// DeleteFromBlock removes every event at or after fromBlock for (chainID,
	// address) and returns how many it removed. The indexer uses it for reorg
	// rollback.
	DeleteFromBlock(ctx context.Context, chainID int64, address string, fromBlock uint64) (int, error)
	ListByName(ctx context.Context, chainID int64, address, name string) ([]*models.ChainEvent, error)
	// ListAll returns every stored event for chainID across all addresses, including
	// rows marked Removed. The event-derived transaction projector replays all events
	// to detect reorgs, as project.ReconcileSecurity does. It is unbounded and not
	// meant for paginated API reads, which is acceptable for a single deployment's
	// event history.
	ListAll(ctx context.Context, chainID int64) ([]*models.ChainEvent, error)
}

// PurchaseRepository persists Vault.buy fills
type PurchaseRepository interface {
	Create(ctx context.Context, p *models.Purchase) error
	List(ctx context.Context) ([]*models.Purchase, error)
	// DeleteAll removes every record. Only maintenance run with the server stopped,
	// such as cmd/reindex, may use it. sales.Service.Reconcile runs while the API
	// serves reads, and deleting then reinserting would expose an empty or partial
	// history, so it uses Upsert and DeleteStaleGeneration.
	DeleteAll(ctx context.Context) error
	// Upsert creates or replaces the record with id p.ID. An existing record is
	// updated in place, so a concurrent List never sees a gap for that id.
	Upsert(ctx context.Context, p *models.Purchase) error
	// DeleteStaleGeneration removes every record whose Generation is not gen. Call it
	// after every surviving record has been upserted with Generation set to gen (see
	// sales.Service.Reconcile).
	DeleteStaleGeneration(ctx context.Context, gen int64) error
	// ListPage returns one page, newest block first, using keyset pagination, with
	// the sort and limit applied in the query. cursor is "" for the first page or a
	// KeysetCursor returned earlier as nextCursor, and a malformed cursor is treated as
	// "". nextCursor is "" when there is no further page
	ListPage(ctx context.Context, cursor string, limit int) (items []*models.Purchase, nextCursor string, err error)
}

// RedemptionRequestRepository persists the redemption read model. Status must
// only be set from reconstructed chain events.
type RedemptionRequestRepository interface {
	// Upsert creates or replaces the record with id r.ID. Callers that rebuild the
	// read model by generation (redemption.Service.Reconcile) set r.Generation to the
	// current generation first.
	Upsert(ctx context.Context, r *models.RedemptionRequest) error
	Get(ctx context.Context, id string) (*models.RedemptionRequest, error)
	List(ctx context.Context, status string) ([]*models.RedemptionRequest, error)
	// DeleteAll removes every entry, for maintenance run with the server stopped,
	// such as cmd/reindex. See PurchaseRepository.DeleteAll.
	DeleteAll(ctx context.Context) error
	// DeleteStaleGeneration removes every record whose Generation is not gen. See
	// PurchaseRepository.DeleteStaleGeneration.
	DeleteStaleGeneration(ctx context.Context, gen int64) error
	// ListPage is PurchaseRepository.ListPage sorted by newest CreatedAt first. A
	// non-empty status keeps only that status. A non-empty address keeps only requests
	// whose beneficiary equals it, ignoring case, so an investor UI can list one
	// beneficiary's requests.
	ListPage(ctx context.Context, status, address, cursor string, limit int) (items []*models.RedemptionRequest, nextCursor string, err error)
}

// WalletSessionRepository persists investor wallet sessions for
// internal/auth.SessionManager, which generates the token and stores it here. The
// MongoDB implementation is shared by server replicas, so a session issued by one
// validates on all of them and a logout revokes it everywhere. It expires entries
// with a TTL index. The in-memory implementation, used with PERSISTENCE_MODE=memory,
// is per process.
type WalletSessionRepository interface {
	Create(ctx context.Context, s *models.WalletSession) error
	// Get returns ErrNotFound for an unknown or expired token, and callers must not
	// distinguish the two in a response (see auth.SessionManager.Validate).
	// Implementations check ExpiresAt themselves, because the MongoDB TTL monitor
	// runs only about once a minute and a token could otherwise validate after it
	// expired.
	Get(ctx context.Context, token string) (*models.WalletSession, error)
	// Delete revokes token. It returns nil for an unknown or expired token, so logout
	// is idempotent.
	Delete(ctx context.Context, token string) error
}

// AdminChallengeRepository persists single-use admin wallet-login challenges, one
// active per address (see models.AdminChallenge), for
// internal/auth.AdminChallengeService, which generates the nonce and message. The
// MongoDB implementation is shared, so a challenge issued by one server replica
// verifies on every replica.
type AdminChallengeRepository interface {
	// Upsert stores c as the active challenge for c.Address and replaces any earlier
	// one, used or not. The verify step carries no client-supplied nonce to choose
	// between several.
	Upsert(ctx context.Context, c *models.AdminChallenge) error
	// Get returns ErrNotFound for an unknown or expired challenge. Implementations
	// check ExpiresAt themselves instead of relying on a TTL sweep, as in
	// WalletSessionRepository.Get.
	Get(ctx context.Context, address string) (*models.AdminChallenge, error)
	// MarkUsed sets Used from false to true in one atomic conditional update, so two
	// concurrent verifications of the same signature cannot both succeed. It returns
	// ErrAlreadyExists if the challenge was already used and ErrNotFound if none
	// exists for address.
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

// BlockHashRepository persists a bounded history of block hashes that the indexer
// walks back through to find the common ancestor of a reorg deeper than its fixed
// rollback window. Record overwrites the hash for a height it already has, and
// PruneBefore keeps the history within the indexer's retention window.
type BlockHashRepository interface {
	Record(ctx context.Context, chainID int64, blockNumber uint64, hash string) error
	// Get returns ErrNotFound for a height that was never recorded or has been pruned
	Get(ctx context.Context, chainID int64, blockNumber uint64) (string, error)
	PruneBefore(ctx context.Context, chainID int64, keepFrom uint64) error
}

// NonceLeaseRepository persists distributed leases over a signer's nonce sequence,
// which let replicas share a hot key (see models.NonceLease for the key format). It
// uses the same fencing pattern as IdempotencyRepository. Only Acquire is
// contended, and Renew and Release apply only to the holder that last won
// Acquire.
type NonceLeaseRepository interface {
	// Acquire grants the lease for key to holderID for ttl if no lease exists for key
	// or the existing one has expired, even if holderID held it, so a live holder
	// extends it with Renew and not with Acquire. On success it returns a token
	// greater than every token issued for key before, starting at 1, and true. If a
	// live lease exists, including one a concurrent caller just won, it returns
	// (0, false, nil) and changes nothing, and the caller must not proceed as if it
	// held the lease.
	Acquire(ctx context.Context, key, holderID string, ttl time.Duration) (token uint64, acquired bool, err error)
	// Renew sets key's expiry to ttl from now if its holder and token equal holderID
	// and token. After another Acquire takes over the key, Renew returns (false, nil)
	// for the old holder and token. The old holder then no longer owns the key and must
	// abort what it was about to persist or broadcast, and not retry the Renew.
	Renew(ctx context.Context, key, holderID string, token uint64, ttl time.Duration) (bool, error)
	// Release frees key for the next Acquire if its holder and token equal holderID
	// and token, so other replicas do not wait out the ttl. Otherwise it returns
	// ErrFencingTokenMismatch and changes nothing.
	Release(ctx context.Context, key, holderID string, token uint64) error
}

// BlockHashEntry is one (height, hash) pair recorded in a chunk commit
type BlockHashEntry struct {
	BlockNumber uint64
	Hash        string
}

// ChunkCommit is everything one indexer scan chunk writes, committed atomically:
// the decoded events, the block hashes to retain, an optional prune boundary, and
// the advanced checkpoint. It applies only if the stored checkpoint is still at
// ExpectedCheckpointVersion.
type ChunkCommit struct {
	ChainID                   int64
	Events                    []*models.ChainEvent
	BlockHashes               []BlockHashEntry
	PruneBlockHashesBefore    *uint64
	Checkpoint                *models.IndexerCheckpoint
	ExpectedCheckpointVersion int
}

// ChunkRewind is ChunkCommit's counterpart for moving backwards: the event
// deletion from DeleteFromBlock upward for every watched address, and the rewound
// checkpoint, written atomically under the same version check. Without the check, a
// forward commit landing between the delete and the checkpoint write would be
// buried by the rewind, leaving fork events above a checkpoint that no later
// rollback reaches.
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

// ChainChunkRepository commits one indexer scan chunk atomically. The MongoDB
// implementation uses a transaction with majority write concern, which requires a
// replica set, and applies the checkpoint update only if the stored version still
// matches the expected one.
type ChainChunkRepository interface {
	// CommitChunk writes c.Events, c.BlockHashes, the optional prune and c.Checkpoint
	// as one atomic unit. An event that already exists is skipped, as in
	// ChainEventRepository.Create. It applies only if the stored checkpoint's Version
	// equals c.ExpectedCheckpointVersion, and then stores c.Checkpoint with Version
	// ExpectedCheckpointVersion+1. It returns (false, nil) on a version mismatch,
	// where another indexer advanced the checkpoint, so the caller discards the chunk
	// and polls again. It returns an error only on a storage failure, in which case
	// nothing was written.
	CommitChunk(ctx context.Context, c ChunkCommit) (committed bool, err error)

	// RewindChunk deletes every event at or above r.DeleteFromBlock for each of
	// r.Addresses and stores r.Checkpoint as one atomic unit, under the same version
	// check as CommitChunk, with Version ExpectedCheckpointVersion+1. It returns
	// (false, nil) on a version mismatch: a concurrent writer moved the checkpoint, so
	// the rewind must be recomputed and not forced. It returns an error only on a
	// storage failure, in which case nothing was written.
	RewindChunk(ctx context.Context, r ChunkRewind) (committed bool, err error)
}

// PublicationRepository persists models.PublicationRecord
type PublicationRepository interface {
	Get(ctx context.Context, id string) (*models.PublicationRecord, error)
	Upsert(ctx context.Context, r *models.PublicationRecord) error
	List(ctx context.Context) ([]*models.PublicationRecord, error)
}

// DeadLetterRepository persists blocks, transactions and logs the indexer could not
// process. Record upserts by e.ID. The first failure of an id stores e as given,
// and a repeat failure increments RetryCount, keeps FirstFailedAt, takes the new
// LastFailedAt and ErrorMessage, and clears Resolved, so a log that fails on every
// poll is one growing entry.
type DeadLetterRepository interface {
	Record(ctx context.Context, e *models.DeadLetterEntry) error
	Get(ctx context.Context, id string) (*models.DeadLetterEntry, error)
	List(ctx context.Context) ([]*models.DeadLetterEntry, error)
	// Resolve marks id Resolved, after a successful operator retry or a dismissal (see
	// models.DeadLetterEntry.Resolved)
	Resolve(ctx context.Context, id string) error
}

// IdempotencyRepository persists the responses of completed state-changing
// requests, keyed by the client's Idempotency-Key.
//
// Reserve and Complete replace a Get followed by a Save, because two concurrent
// requests with the same key could both Get "not found" and both run the
// handler's side effect, such as submitting a transaction. Only Reserve is
// contended, and exactly one caller wins.
type IdempotencyRepository interface {
	Get(ctx context.Context, key string) (*models.IdempotencyRecord, error)
	// Reserve atomically claims an absent or expired key, stores ResponseStatus 0
	// with a fresh token and returns (nil, true, token, nil). The winner must run
	// the handler, then Complete or Release with that token.
	//
	// A live pending or completed record returns (existing, false, "", nil);
	// the caller must not run the handler. Expiry permits takeover after a crash
	// or delay. Complete and Release reject stale tokens with
	// ErrFencingTokenMismatch so the former owner cannot alter the new reservation.
	//
	// Checking and claiming must be one atomic step, such as a unique-index insert
	// or an upsert filtered on expiry, rather than a read followed by a write.
	Reserve(ctx context.Context, key, method, path, requestHash string, ttl time.Duration) (existing *models.IdempotencyRecord, reserved bool, token string, err error)
	// Complete stores the response on the record that Reserve created, matched by key
	// and token. Call it at most once per successful Reserve. It returns
	// ErrFencingTokenMismatch, writing nothing, if the current record for key is
	// missing or has a different token.
	Complete(ctx context.Context, key, token string, status int, body []byte) error
	// Release deletes the reservation when the handler produced no cacheable outcome,
	// which is any non-2xx response, since only successful side effects are cached.
	// Otherwise the key would stay in progress and block every retry. It matches by
	// key and token, and returns ErrFencingTokenMismatch, deleting nothing, on a
	// mismatch.
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
