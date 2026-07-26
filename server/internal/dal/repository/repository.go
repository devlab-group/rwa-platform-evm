// Package repository defines storage interfaces for the persisted
// collections. Business logic depends only on these interfaces so it
// can be tested with the in-memory implementation in
// internal/dal/memory without a live MongoDB.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
)

// ErrNotFound is returned when a lookup by ID finds nothing.
var ErrNotFound = errors.New("repository: not found")

// ErrAlreadyExists is returned by Create when the identity already exists.
var ErrAlreadyExists = errors.New("repository: already exists")

// ErrFencingTokenMismatch is returned by IdempotencyRepository.Complete/
// Release when the caller's token no longer matches the record's current
// owner: either the reservation expired and was
// taken over by a fresh Reserve before this (slow) caller finished, or the
// key was never reserved at all under that token. Callers must treat this
// as "not mine to finish" and stop, never retry the same write blindly —
// see Reserve/Complete/Release's doc comments on IdempotencyRepository.
var ErrFencingTokenMismatch = errors.New("repository: idempotency fencing token mismatch")

// ProjectRepository persists the single project record. Deployment is now
// broadcast from the admin's wallet and only OBSERVED by the server
// (project.ReconcileDeployment folds the on-chain ProjectDeployed event into
// this singleton), so there is no server-side pre-broadcast reservation to
// guard — the Mongo document's fixed technical _id keeps "one project per
// deployment" a real database uniqueness guarantee regardless (see mongodb's
// projectDocID).
type ProjectRepository interface {
	Get(ctx context.Context) (*models.Project, error)
	Upsert(ctx context.Context, p *models.Project) error
}

// AssetProfileRepository persists the immutable-per-deployment asset
// profile. Create is create-once/CAS (returns ErrAlreadyExists for a
// projectId that already has a stored profile) — the ONLY supported way to
// persist a profile (POST /api/v1/profile/validate must be pure, with no
// upsert side effect). Upsert is kept only for
// migration/backfill call sites that already hold a verified record (e.g.
// cmd/reindex); ordinary API request handlers must use Create.
type AssetProfileRepository interface {
	Get(ctx context.Context, projectID string) (*models.AssetProfile, error)
	// GetCurrent returns the deployment's single current Asset Profile without
	// a projectId (single-tenant / one-asset-per-deployment). When more than
	// one exists it returns the most recent by CreatedAt; repository.ErrNotFound
	// when none exists yet.
	GetCurrent(ctx context.Context) (*models.AssetProfile, error)
	Create(ctx context.Context, p *models.AssetProfile) error
	Upsert(ctx context.Context, p *models.AssetProfile) error
}

// AssetRecordRepository persists tokenization records.
type AssetRecordRepository interface {
	List(ctx context.Context) ([]*models.AssetRecord, error)
	Get(ctx context.Context, recordID string) (*models.AssetRecord, error)
	Create(ctx context.Context, r *models.AssetRecord) error
	Update(ctx context.Context, r *models.AssetRecord) error
	// UpdateConditional writes r only if the CURRENTLY stored record's Version
	// equals expectedVersion, then increments Version — this serializes
	// reissue and relay by record id at the storage layer, not just in
	// memory. Returns (false, nil) on a version mismatch — the caller lost a
	// race to a concurrent reissue/relay and MUST re-read/re-decide rather
	// than blindly retry — and ErrNotFound if the record doesn't exist. Mirror
	// of TransactionRepository.UpdateConditional.
	UpdateConditional(ctx context.Context, r *models.AssetRecord, expectedVersion int) (bool, error)
	// ListPage returns one bounded, ascending-CreatedAt page via
	// repository-level keyset pagination — see
	// PurchaseRepository.ListPage's doc comment; this collection keeps
	// its pre-existing oldest-first order, unlike purchases/redemption
	// requests). cursor is "" for the first page. nextCursor is "" once
	// there is no further page.
	ListPage(ctx context.Context, cursor string, limit int) (items []*models.AssetRecord, nextCursor string, err error)
}

// AuditPackageRepository persists .rwa package build metadata.
type AuditPackageRepository interface {
	Get(ctx context.Context, recordID string) (*models.AuditPackage, error)
	Upsert(ctx context.Context, p *models.AuditPackage) error
}

// AttestationRepository persists verified signed-results.
type AttestationRepository interface {
	Get(ctx context.Context, recordID string) (*models.Attestation, error)
	Upsert(ctx context.Context, a *models.Attestation) error
}

// InvestorRepository persists wallet compliance status.
type InvestorRepository interface {
	Get(ctx context.Context, address string) (*models.Investor, error)
	List(ctx context.Context) ([]*models.Investor, error)
	Upsert(ctx context.Context, inv *models.Investor) error
	// ListPage returns one bounded, ascending-address page via
	// repository-level keyset pagination — the database query itself is
	// bounded, unlike List above. cursor is "" for the first page, or
	// the previous page's last Address to continue past it — Address is
	// already this collection's unique key, so unlike Purchase/
	// RedemptionRequest's ListPage no separate KeysetCursor/tiebreak
	// encoding is needed. nextCursor is "" once there is no further page.
	ListPage(ctx context.Context, cursor string, limit int) (items []*models.Investor, nextCursor string, err error)
}

// WalletChallengeRepository persists one-time wallet-ownership challenges.
type WalletChallengeRepository interface {
	Create(ctx context.Context, c *models.WalletChallenge) error
	Get(ctx context.Context, id string) (*models.WalletChallenge, error)
	// MarkUsed atomically transitions Used false->true (compare-and-swap,
	// not an unconditional set): two concurrent Verify calls presenting the
	// same valid signature for the same nonce must not both succeed, since
	// this is what makes the challenge genuinely single-use rather than
	// single-use "in practice, usually." Implementations MUST make the
	// "is it currently unused" check and the write atomic (a single
	// conditional update, not a separate read then write). Returns
	// ErrAlreadyExists if the challenge was already used (by a prior call
	// or a concurrent one that won the race) and ErrNotFound if id doesn't
	// exist at all.
	MarkUsed(ctx context.Context, id string) error
	// CountActive reports how many challenges for address (already
	// normalized the same way Create's caller normalizes it — EIP-55
	// checksummed hex, an exact match not a case-insensitive one) are
	// currently neither used nor expired as of now — this caps the number of
	// active challenges per normalized address.
	// ChallengeService.Create uses this to reject once an address has
	// accumulated too many outstanding challenges, bounding how much
	// storage an anonymous caller can pin for a single address regardless
	// of the wallet_challenges TTL index.
	CountActive(ctx context.Context, address string, now time.Time) (int, error)
}

// KYCEventRepository persists the durable webhook inbox/outbox, so a webhook
// decision is consumed before its on-chain status is durably applied. Create
// enforces TWO independent uniqueness constraints
// atomically: the raw payload hash (a byte-for-byte replay)
// AND (Provider,EventID) (the same logical event reserialized/re-signed as
// different bytes) — either collision returns ErrAlreadyExists.
type KYCEventRepository interface {
	// Exists reports whether an event with this payload hash was already processed.
	Exists(ctx context.Context, payloadHash string) (bool, error)
	Create(ctx context.Context, e *models.KYCEvent) error
	// Update persists a status/TxID transition for an already-created
	// event (ApplyStatus Accepted -> Applying -> Applied/Failed, or ->
	// Superseded — see models.KYCApplyStatus).
	Update(ctx context.Context, e *models.KYCEvent) error
	List(ctx context.Context) ([]*models.KYCEvent, error)
	// ListPending returns every event still ApplyStatusClaiming,
	// ApplyStatusAccepted, or ApplyStatusApplying — the reconciler's work
	// queue. Claiming is included so an event durably created but never
	// finalized (Process died at the claim/finalize boundary) is recovered.
	ListPending(ctx context.Context) ([]*models.KYCEvent, error)
	// ClaimLatestForAddress atomically records (occurredAt,eventKey) as
	// the current winning decision for address IFF it is strictly newer
	// (by (occurredAt,eventKey) lexicographic order, the defined ordering
	// for newer/older decisions) than whatever is currently
	// claimed, or nothing is claimed yet. Returns (true, nil) if this
	// caller's event won the claim (proceed to durably store it as
	// Accepted); (false, nil) if a newer-or-equal decision already holds
	// the claim (the caller must reject this delivery as stale). This is
	// the single atomic operation that replaces the old racy
	// LatestForAddress-then-Create sequence, where two concurrent distinct
	// events for the same address could both pass a separate freshness
	// check before either had persisted.
	ClaimLatestForAddress(ctx context.Context, address string, occurredAt time.Time, eventKey string) (bool, error)
	// CurrentClaimEventKey returns the eventKey currently holding the
	// claim for address, or ErrNotFound if none. Used by the reconciler to
	// detect that an Accepted/Applying event has since been superseded by
	// a newer decision and must not be applied on-chain.
	CurrentClaimEventKey(ctx context.Context, address string) (string, error)
}

// ComplianceOperationRepository persists submitted compliance status transactions.
type ComplianceOperationRepository interface {
	Create(ctx context.Context, op *models.ComplianceOperation) error
	List(ctx context.Context) ([]*models.ComplianceOperation, error)
}

// TransactionRepository persists transaction-manager state.
type TransactionRepository interface {
	Create(ctx context.Context, tx *models.Transaction) error
	Get(ctx context.Context, id string) (*models.Transaction, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*models.Transaction, error)
	// GetByTxHash returns the transaction record with this txHash on chainID,
	// or ErrNotFound when none exists. Used by the event-derived stack-
	// transactions projector (txindex.ReconcileStackTransactions) to dedup
	// against tx-manager-submitted records so a server-relayed tx is not
	// double-listed. In practice at most one record holds any given txHash
	// (a hash uniquely identifies one on-chain tx and thus one submitter);
	// if both a manager and an event-derived record ever coexist, the
	// manager-submitted (non-EventDerived) one is returned so dedup triggers.
	GetByTxHash(ctx context.Context, chainID int64, txHash string) (*models.Transaction, error)
	// Update is an unconditional last-writer-wins replace, kept for call
	// sites that already hold exclusive access to tx by construction (e.g.
	// Submit, under both the in-process per-signer lock and — in
	// mongo-lease mode — a freshly Renew-verified lease). Prefer
	// UpdateConditional for any write that could otherwise race a
	// concurrent/superseded writer — see its doc comment.
	Update(ctx context.Context, tx *models.Transaction) error
	// UpdateConditional writes tx only if the CURRENTLY stored record's
	// Version equals expectedVersion, then increments Version. This lets a
	// caller update conditionally on (id, expectedStatus, version,
	// fencingToken) and reject a stale token at the repository write itself,
	// not only in a prior Renew call. Returns (false, nil) on a version
	// mismatch (nothing written, not an error — the caller lost a race and
	// must re-read/re-decide, not blindly retry the same write) and
	// ErrNotFound if id doesn't exist at all.
	UpdateConditional(ctx context.Context, tx *models.Transaction, expectedVersion int) (bool, error)
	List(ctx context.Context) ([]*models.Transaction, error)
	ListByStatus(ctx context.Context, status models.TxStatus) ([]*models.Transaction, error)
	// ListPage returns one bounded, ascending-SubmittedAt page via
	// repository-level keyset pagination (see
	// AssetRecordRepository.ListPage's doc comment), optionally filtered
	// to transactions where From or To equals address (empty = no
	// filter, matching listTransactions' existing semantics). cursor is
	// "" for the first page. nextCursor is "" once there is no further
	// page.
	ListPage(ctx context.Context, address, cursor string, limit int) (items []*models.Transaction, nextCursor string, err error)
}

// ChainEventRepository persists indexed events, keyed by
// (chainId, address, txHash, logIndex).
type ChainEventRepository interface {
	Exists(ctx context.Context, key models.EventKey) (bool, error)
	Create(ctx context.Context, e *models.ChainEvent) error
	// DeleteFromBlock removes every event at or after fromBlock for
	// (chainID, address), used for reorg rollback.
	DeleteFromBlock(ctx context.Context, chainID int64, address string, fromBlock uint64) (int, error)
	ListByName(ctx context.Context, chainID int64, address, name string) ([]*models.ChainEvent, error)
	// ListAll returns EVERY stored event for chainID across all addresses,
	// INCLUDING soft-removed (reorged-out) rows — the event-derived stack-
	// transactions projector (txindex.ReconcileStackTransactions) needs them
	// to detect reorgs by full replay, mirroring the other full-replay
	// reconcilers (project.ReconcileSecurity). Unbounded, like
	// RedemptionRequestRepository.List: safe for V1's single-tenant,
	// single-deployment scale where one issuer's whole event history fits
	// comfortably in memory; it is NOT a paginated API-facing read.
	ListAll(ctx context.Context, chainID int64) ([]*models.ChainEvent, error)
}

// PurchaseRepository persists Vault.buy fills.
type PurchaseRepository interface {
	Create(ctx context.Context, p *models.Purchase) error
	List(ctx context.Context) ([]*models.Purchase, error)
	// DeleteAll clears the read model, for out-of-band maintenance
	// (cmd/reindex) that runs with the server stopped. sales.Service.
	// Reconcile — which runs on a LIVE 15s ticker with API readers
	// active — MUST NOT use this: a DeleteAll-then-reinsert
	// rebuild lets a concurrent reader observe an empty or partial
	// history. It uses Upsert/DeleteStaleGeneration below instead.
	DeleteAll(ctx context.Context) error
	// Upsert creates or replaces the record keyed by p.ID — the reader-
	// safe half of a generation-swap rebuild: an existing
	// id is updated in place rather than deleted-then-recreated, so a
	// concurrent List() never observes a gap for any id this call
	// touches.
	Upsert(ctx context.Context, p *models.Purchase) error
	// DeleteStaleGeneration removes every record whose Generation != gen
	// — the cleanup half of a generation-swap rebuild, safe to call only
	// AFTER every surviving record has been Upsert'd with Generation set
	// to gen (see sales.Service.Reconcile).
	DeleteStaleGeneration(ctx context.Context, gen int64) error
	// ListPage returns one bounded, newest-block-first page via
	// repository-level keyset pagination and bounded Mongo queries: unlike
	// List above, this pushes both the sort and the limit into the query
	// itself rather than fetching everything and slicing in the caller.
	// cursor is "" for the first page, or a
	// KeysetCursor previously returned as nextCursor to continue past it;
	// an unrecognized/malformed cursor is treated as "" (first page).
	// nextCursor is "" once there is no further page.
	ListPage(ctx context.Context, cursor string, limit int) (items []*models.Purchase, nextCursor string, err error)
}

// RedemptionRequestRepository persists the redemption read model. Status
// MUST only be set from reconstructed chain events.
type RedemptionRequestRepository interface {
	// Upsert creates or replaces the record keyed by r.ID. Callers that
	// participate in a generation-swap rebuild (redemption.Service.
	// Reconcile) MUST set r.Generation to the rebuild's current
	// generation before calling this — see DeleteStaleGeneration and
	// PurchaseRepository's doc comment for the full pattern.
	Upsert(ctx context.Context, r *models.RedemptionRequest) error
	Get(ctx context.Context, id string) (*models.RedemptionRequest, error)
	List(ctx context.Context, status string) ([]*models.RedemptionRequest, error)
	// DeleteAll removes every read-model entry, for out-of-band
	// maintenance (cmd/reindex) that runs with the server stopped — see
	// PurchaseRepository.DeleteAll's doc comment; Reconcile's live ticker
	// path uses DeleteStaleGeneration instead.
	DeleteAll(ctx context.Context) error
	// DeleteStaleGeneration removes every record whose Generation != gen
	// — see PurchaseRepository.DeleteStaleGeneration's doc comment.
	DeleteStaleGeneration(ctx context.Context, gen int64) error
	// ListPage mirrors PurchaseRepository.ListPage, sorted
	// newest-CreatedAt-first instead of newest-block-first. It is
	// optionally filtered by status and by address (the request's
	// beneficiary; empty = no filter), the latter matched
	// case-insensitively like TransactionRepository.ListPage's address
	// filter so a public investor UI can list one beneficiary's requests.
	ListPage(ctx context.Context, status, address, cursor string, limit int) (items []*models.RedemptionRequest, nextCursor string, err error)
}

// WalletSessionRepository persists investor wallet sessions in a shared
// Mongo store with a TTL index. Backing internal/auth.SessionManager, which
// generates the
// random token and delegates storage here — the mongodb implementation
// makes a token issued by one server replica validate, and a logout on
// any replica revoke it everywhere, immediately; the memory implementation
// (used automatically for PERSISTENCE_MODE=memory) keeps the original
// single-process behavior for local/dev use.
type WalletSessionRepository interface {
	Create(ctx context.Context, s *models.WalletSession) error
	// Get returns repository.ErrNotFound for an unknown OR expired token —
	// callers must not distinguish those cases in a response (see
	// auth.SessionManager.Validate). Implementations MUST check ExpiresAt
	// themselves rather than relying solely on a backing store's
	// background TTL sweep (Mongo's TTL monitor runs on its own ~60s
	// cycle, so a token past its ExpiresAt can otherwise still be found
	// for up to a minute after it should have stopped validating).
	Get(ctx context.Context, token string) (*models.WalletSession, error)
	// Delete revokes token. A no-op (nil error) for an unknown/expired
	// token, so logout is idempotent.
	Delete(ctx context.Context, token string) error
}

// AdminChallengeRepository persists single-use admin wallet-login challenges
// (one active per address; see models.AdminChallenge). Backing
// internal/auth.AdminChallengeService, which generates the nonce/message and
// delegates storage here so a challenge issued by one server replica verifies
// on every replica sharing the store.
type AdminChallengeRepository interface {
	// Upsert stores c as THE active challenge for c.Address, replacing any
	// prior one (used or not) — there is exactly one active admin challenge
	// per address, since the verify step has no client-echoed nonce to
	// disambiguate several.
	Upsert(ctx context.Context, c *models.AdminChallenge) error
	// Get returns repository.ErrNotFound for an unknown OR expired challenge.
	// Implementations MUST check ExpiresAt themselves rather than relying on
	// a background TTL sweep (same reasoning as WalletSessionRepository.Get).
	Get(ctx context.Context, address string) (*models.AdminChallenge, error)
	// MarkUsed atomically transitions Used false->true (compare-and-swap, not
	// an unconditional set) so two concurrent verifies of the same signature
	// cannot both succeed. Returns ErrAlreadyExists if it was already used and
	// ErrNotFound if no challenge exists for address.
	MarkUsed(ctx context.Context, address string) error
}

// AuditLogRepository persists append-only operational audit entries.
type AuditLogRepository interface {
	Append(ctx context.Context, e *models.AuditLogEntry) error
	// List returns the most recent entries, optionally filtered by
	// category (empty string means all categories).
	List(ctx context.Context, category string, limit int) ([]*models.AuditLogEntry, error)
}

// IndexerCheckpointRepository persists per-(chainId,address) scan progress.
type IndexerCheckpointRepository interface {
	Get(ctx context.Context, chainID int64, address string) (*models.IndexerCheckpoint, error)
	Set(ctx context.Context, c *models.IndexerCheckpoint) error
}

// BlockHashRepository persists a bounded per-block hash history the indexer
// uses to walk back to a true common ancestor on a reorg deeper than a
// single fixed rollback window. Record is
// idempotent (re-recording the same height just overwrites its hash).
// PruneBefore bounds the repository's size to the indexer's configured
// retention window instead of growing forever.
type BlockHashRepository interface {
	Record(ctx context.Context, chainID int64, blockNumber uint64, hash string) error
	// Get returns ErrNotFound for a height that was never recorded or has
	// already been pruned — both mean "no opinion", not "block had no hash".
	Get(ctx context.Context, chainID int64, blockNumber uint64) (string, error)
	PruneBefore(ctx context.Context, chainID int64, keepFrom uint64) error
}

// NonceLeaseRepository persists distributed leases over a signer's nonce
// sequence (multi-replica hot-key coordination —
// see models.NonceLease's doc comment for the key format). Mirrors
// IdempotencyRepository's Reserve/Complete/Release fencing pattern above:
// Acquire is the only operation two racing callers may contend over, and
// Renew/Release are only ever valid for the caller that most recently won
// Acquire.
type NonceLeaseRepository interface {
	// Acquire atomically grants the lease for key to holderID for ttl, IFF
	// no lease currently exists for key or the existing one has expired
	// (regardless of who held it, including holderID itself — a holder
	// wanting to extend an already-live lease it holds must use Renew, not
	// call Acquire again). Returns (token, true, nil) on a successful
	// grant, where token is STRICTLY GREATER than any token previously
	// issued for key (0 has never been a valid token — the very first
	// grant for a key returns 1). Returns (0, false, nil), with nothing
	// changed, if key is currently held live by anyone (including a
	// concurrent caller that raced to Acquire first) — the caller MUST NOT
	// proceed as if it held the lease.
	Acquire(ctx context.Context, key, holderID string, ttl time.Duration) (token uint64, acquired bool, err error)
	// Renew extends key's expiry by ttl IFF it is currently held by
	// holderID with exactly this token — this is also the mechanism that
	// DETECTS a superseded holder by rejecting a state write whose fencing
	// token is older than the current lease's: once
	// another Acquire has taken over key (because this holder's lease
	// expired), holderID/token no longer match the live record, and every
	// subsequent Renew for the old (holderID, token) pair returns
	// (false, nil) — the old holder must treat this as "I no longer own
	// this key" and abort whatever it was about to persist/broadcast,
	// never retry the same Renew call expecting a different outcome.
	Renew(ctx context.Context, key, holderID string, token uint64, ttl time.Duration) (bool, error)
	// Release relinquishes key IFF it is currently held by holderID with
	// exactly this token, so a normal (non-crashed) holder frees the lease
	// promptly for the next Acquire rather than making every other replica
	// wait out the full ttl. Returns ErrFencingTokenMismatch (without
	// changing anything) if key is not currently held by (holderID,
	// token) — e.g. it already expired and was taken over — mirroring
	// IdempotencyRepository.Release's fencing semantics exactly.
	Release(ctx context.Context, key, holderID string, token uint64) error
}

// BlockHashEntry is one (height, hash) pair recorded as part of an atomic
// chunk commit.
type BlockHashEntry struct {
	BlockNumber uint64
	Hash        string
}

// ChunkCommit bundles everything a single indexer scan chunk writes so a
// repository can persist it atomically: the validated,
// already-decoded events; the retained block-hash history for the chunk; an
// optional prune boundary; and the advanced checkpoint — all or nothing,
// conditioned on the stored checkpoint not having moved under a concurrent
// writer since ExpectedCheckpointVersion.
type ChunkCommit struct {
	ChainID                   int64
	Events                    []*models.ChainEvent
	BlockHashes               []BlockHashEntry
	PruneBlockHashesBefore    *uint64
	Checkpoint                *models.IndexerCheckpoint
	ExpectedCheckpointVersion int
}

// ChunkRewind is ChunkCommit's backwards counterpart: everything a reorg
// rollback or an operator's trusted reset writes — the event delete from
// DeleteFromBlock upward across every watched address, plus the rewound
// checkpoint — bundled so a repository can persist it atomically under the
// same version fence.
//
// Without the fence the rewind is the hole in the multi-replica story: a
// concurrent forward commit can land between the delete and the checkpoint
// write, and the unconditional rewind then buries it — leaving that commit's
// fork events behind a checkpoint low enough that no later rollback covers
// them.
type ChunkRewind struct {
	ChainID int64
	// Addresses are the watched contract addresses to delete events for.
	Addresses []string
	// DeleteFromBlock deletes every event at or above this block number.
	DeleteFromBlock uint64
	// Checkpoint is the rewound checkpoint to store.
	Checkpoint                *models.IndexerCheckpoint
	ExpectedCheckpointVersion int
}

// ChainChunkRepository atomically commits one indexer scan chunk: a single
// repository operation writes the validated events, hash history, pruning,
// and checkpoint in a Mongo transaction with majority write concern, and
// makes the checkpoint update conditional on the expected prior
// checkpoint/version. Before this, the indexer wrote each of
// those through separate repository calls, so a crash or write failure
// mid-commit could leave orphaned fork events behind a still-old checkpoint
// that a later reorg would never roll back.
type ChainChunkRepository interface {
	// CommitChunk writes c.Events (idempotently — a duplicate
	// (chainId,address,txHash,logIndex) is a no-op, matching
	// ChainEventRepository.Create), c.BlockHashes, the optional
	// PruneBlockHashesBefore delete, and c.Checkpoint as ONE atomic unit. It
	// succeeds only if the currently-stored checkpoint's Version equals
	// c.ExpectedCheckpointVersion, then persists c.Checkpoint with Version =
	// ExpectedCheckpointVersion+1. Returns (false, nil) on a version mismatch
	// (a concurrent indexer advanced the checkpoint first — the caller lost
	// the race, must discard this chunk, and re-poll), and a non-nil error
	// only for an actual storage failure (in which case NOTHING was
	// committed).
	CommitChunk(ctx context.Context, c ChunkCommit) (committed bool, err error)

	// RewindChunk deletes every event at or above r.DeleteFromBlock for each
	// of r.Addresses and stores r.Checkpoint as ONE atomic unit, under the
	// same version fence CommitChunk uses: it succeeds only if the stored
	// checkpoint's Version still equals r.ExpectedCheckpointVersion, then
	// persists r.Checkpoint with Version = ExpectedCheckpointVersion+1.
	// Returns (false, nil) on a version mismatch — a concurrent writer moved
	// the checkpoint, so this rewind is computed against a state that no
	// longer exists and must be recomputed rather than forced through — and a
	// non-nil error only for an actual storage failure (nothing written).
	RewindChunk(ctx context.Context, r ChunkRewind) (committed bool, err error)
}

// PublicationRepository persists models.PublicationRecord.
type PublicationRepository interface {
	Get(ctx context.Context, id string) (*models.PublicationRecord, error)
	Upsert(ctx context.Context, r *models.PublicationRecord) error
	List(ctx context.Context) ([]*models.PublicationRecord, error)
}

// DeadLetterRepository persists blocks/transactions/logs the indexer could
// not process. Record is an upsert keyed by
// e.ID: a first failure inserts RetryCount 0 with FirstFailedAt==LastFailedAt;
// a repeat failure for the SAME id increments RetryCount and advances
// LastFailedAt/ErrorMessage while preserving the original FirstFailedAt —
// so a log that fails on every poll produces one growing entry, not one new
// row per poll.
type DeadLetterRepository interface {
	Record(ctx context.Context, e *models.DeadLetterEntry) error
	Get(ctx context.Context, id string) (*models.DeadLetterEntry, error)
	List(ctx context.Context) ([]*models.DeadLetterEntry, error)
	// Resolve marks id Resolved (a successful operator retry, or a manual
	// dismissal) — see models.DeadLetterEntry.Resolved.
	Resolve(ctx context.Context, id string) error
}

// IdempotencyRepository persists completed state-changing responses keyed by
// the client-supplied Idempotency-Key.
//
// Reserve/Complete replace a naive Get-then-Save pattern specifically to
// close a race two concurrent requests carrying the same Idempotency-Key
// could otherwise hit: both Get a "not found" before either has Saved,
// so both go on to execute the handler's side effect (e.g. both submit a
// blockchain tx) — defeating the entire point of idempotency, which exists
// precisely for concurrent/retried identical requests. Reserve is the only
// operation implementations may allow two racing callers to contend over;
// exactly one may win (see its doc comment).
type IdempotencyRepository interface {
	Get(ctx context.Context, key string) (*models.IdempotencyRecord, error)
	// Reserve atomically claims key for a new, not-yet-completed request:
	// if no record exists for key, it inserts one with ResponseStatus 0
	// (pending) and returns (nil, true, token, nil) — the caller now owns
	// executing the request and must call Complete/Release with token when
	// done. If a record already exists AND is not expired (pending from
	// another in-flight request, or already completed), Reserve returns
	// that record and (existing, false, "", nil): the caller must NOT
	// execute the handler again. If a record exists but its ExpiresAt has
	// already passed (the original owner crashed or was too slow),
	// Reserve MUST atomically take it over — insert/replace it with a
	// FRESH token — and return (nil, true, token, nil) exactly as for the
	// no-record case, rather than returning the stale record forever
	// (otherwise a crashed pending request stays permanently wedged).
	//
	// token is a fencing/lease token unique to this specific reservation.
	// Without one, an expired reservation could be taken over while an old,
	// slow request still completes it, letting that stale request finish a
	// newer owner's reservation. Complete/Release MUST be called with the
	// SAME token Reserve returned; an implementation MUST reject a
	// Complete/Release carrying a stale token (one belonging to a
	// reservation that has since been taken over) with
	// repository.ErrFencingTokenMismatch instead of applying it, so a slow
	// original owner can never clobber a later owner's write.
	//
	// Implementations MUST make the "does a live (non-expired) record
	// already exist" check and the insert/replace atomic (e.g. a single
	// unique-indexed INSERT, or an upsert filtered on "expired or absent"
	// — not a separate read then write) — that atomicity is the entire
	// guarantee this method exists to provide.
	Reserve(ctx context.Context, key, method, path, requestHash string, ttl time.Duration) (existing *models.IdempotencyRecord, reserved bool, token string, err error)
	// Complete fills in the response on a record Reserve created for this
	// caller, identified by key AND token together. Safe to call at most
	// once per successful Reserve. Returns ErrFencingTokenMismatch (without
	// writing anything) if key's current record either doesn't exist or
	// carries a different token than the one passed in — see Reserve's doc
	// comment.
	Complete(ctx context.Context, key, token string, status int, body []byte) error
	// Release deletes a reservation Reserve created for this caller
	// (identified by key AND token together) when the handler did NOT
	// produce a cacheable outcome (a non-2xx response — only successful
	// side effects are idempotency-cached, matching the pre-existing
	// contract). Without this, a request that legitimately failed would
	// leave its key stuck "in progress" forever, permanently blocking
	// every future retry with that key instead of letting the client
	// simply try again. Returns ErrFencingTokenMismatch (without deleting
	// anything) on a token mismatch, same as Complete.
	Release(ctx context.Context, key, token string) error
}

// Repositories bundles every collection-scoped repository so it can be
// constructed once and passed to workflow/API constructors.
type Repositories struct {
	Projects             ProjectRepository
	AssetProfiles        AssetProfileRepository
	AssetRecords         AssetRecordRepository
	AuditPackages        AuditPackageRepository
	Attestations         AttestationRepository
	Investors            InvestorRepository
	WalletChallenges     WalletChallengeRepository
	KYCEvents            KYCEventRepository
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
