package blockchain

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// FeeMode selects EIP-1559 or legacy transaction construction.
type FeeMode string

const (
	FeeModeEIP1559 FeeMode = "eip1559"
	FeeModeLegacy  FeeMode = "legacy"
)

// SubmitRequest describes one state-changing transaction to sign and
// broadcast. Exactly one of PrivateKey or Signer must be set: PrivateKey
// for simple local/dev/test use (internally adapted through
// StaticKeySigner), Signer for anything backed by internal/keys
// (local-keystore/vault/kms-mock) — see Signer's doc comment for why these
// are two different things, not just two ways to spell the same thing.
type SubmitRequest struct {
	IdempotencyKey string
	Kind           string
	PrivateKey     *ecdsa.PrivateKey
	Signer         Signer
	To             common.Address
	Data           []byte
	Value          *big.Int
	// AllowReorgRetry opts THIS Kind's IdempotencyKey into same-key retry
	// after TxReorged/TxNonceConsumedExternally. Leave false
	// unless the on-chain action this Kind submits is verifiably safe to
	// resubmit under a fresh nonce even if the ORIGINAL attempt might
	// still land separately (e.g. a simple idempotent state write like
	// compliance.StatusService's setStatus — see its call site for the
	// reasoning). Most Kinds — anything that moves value, mints, or has no
	// on-chain replay guard the server can rely on — should leave this
	// false: false means Submit refuses the same-key retry and
	// RefreshStatuses' signer-level guard blocks further submissions for
	// this signer until an operator identifies the canonical transaction
	// at (chainId, sender, nonce) and resolves it.
	AllowReorgRetry bool
}

// signer resolves req's signing capability into a single Signer,
// regardless of which of PrivateKey/Signer the caller populated.
func (req SubmitRequest) signer() (Signer, error) {
	switch {
	case req.Signer != nil:
		return req.Signer, nil
	case req.PrivateKey != nil:
		return NewStaticKeySigner(req.PrivateKey), nil
	default:
		return nil, errors.New("blockchain: SubmitRequest requires PrivateKey or Signer")
	}
}

// ErrAmbiguousBroadcast may be returned (or wrapped) by a Client
// implementation's SendTransaction to explicitly flag "the outcome of this
// broadcast is unknown" — e.g. a proxy/relay that itself timed out talking
// to the node after already having forwarded the request. isAmbiguousBroadcast
// also infers this for the common transport-level cases (context deadline/
// cancellation, network errors, unexpected EOF) without the Client needing
// to opt in explicitly.
var ErrAmbiguousBroadcast = errors.New("blockchain: ambiguous broadcast outcome")

// isAmbiguousBroadcast classifies a SendTransaction error as either a
// DEFINITE rejection — the node synchronously said no (bad nonce,
// underpriced, insufficient funds, ...) and nothing reached the mempool, so
// TxFailed and the idempotency key are safely retryable — or an AMBIGUOUS
// one, where a timeout/connection reset/context deadline means the node may
// well have accepted the transaction and only the acknowledgement was lost.
// The classification deliberately errs toward "ambiguous" for anything that
// looks like a transport-layer failure: wrongly treating an accepted
// transaction as failed risks a duplicate privileged operation under a
// freshly allocated nonce, which is strictly worse than an extra, harmless
// TxBroadcastUnknown resolution
// cycle for a transaction that really did fail outright.
func isAmbiguousBroadcast(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrAmbiguousBroadcast) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

// encodeSignedTx returns the exact signed RLP bytes of tx, "0x"-prefixed
// hex, so the exact signed transaction can be persisted before broadcast.
func encodeSignedTx(tx *types.Transaction) (string, error) {
	b, err := tx.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("blockchain: encode signed transaction: %w", err)
	}
	return "0x" + common.Bytes2Hex(b), nil
}

// decodeSignedTx reverses encodeSignedTx, reconstructing the EXACT
// transaction TxManager originally signed and broadcast, so a
// TxBroadcastUnknown record can be rebroadcast byte-for-byte instead of
// being re-signed (which would change its hash and could allocate a new
// nonce).
func decodeSignedTx(rawHex string) (*types.Transaction, error) {
	b := common.FromHex(rawHex)
	if len(b) == 0 {
		return nil, errors.New("blockchain: no persisted signed transaction bytes to rebroadcast")
	}
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(b); err != nil {
		return nil, fmt.Errorf("blockchain: decode persisted signed transaction: %w", err)
	}
	return tx, nil
}

// TxManager serializes nonces per signing key, builds and submits
// transactions, and tracks their lifecycle.
//
// Two coordination modes (config.Config.TxCoordinationMode selects which):
//
//   - "in-process" (the default): the
//     per-key nonce lock (keyLocks below) only serializes submissions
//     WITHIN one txManager instance, i.e. one running `platform` process.
//     Running two server processes/replicas configured with the SAME hot
//     key is NOT safe — both would call PendingNonceAt independently, can
//     observe the same "next" nonce before either has broadcast, and both
//     submit at that nonce, so one transaction silently loses (dropped or
//     replaced depending on relative gas price). Deployments using this
//     mode MUST run exactly one platform process per configured hot key
//     (compliance/relayer/deployer) — see
//     docs/operator/operator-guide.md's deployment topology section.
//   - "mongo-lease": in addition to the in-process lock (still needed for
//     concurrency WITHIN one process — the lease guards ACROSS processes,
//     it does not replace the intra-process mutex), Submit/Replace acquire
//     a distributed lease (repository.NonceLeaseRepository) for
//     chainId+":"+signerAddress before touching that signer's nonce
//     sequence at all, hold it across allocateNonce -> sign -> persist ->
//     broadcast (re-confirming ownership via Renew immediately before the
//     persist step — see nonceLease's doc comment on renewLease), and
//     Release it when done. A replica that loses the race to acquire, or
//     loses the lease mid-flight (e.g. it stalled long enough for the TTL
//     to expire and another replica took over), fails fast with
//     ErrLeaseUnavailable rather than allocating a nonce or broadcasting —
//     see NewTxManagerWithLease.
type nonceLease struct {
	repo     repository.NonceLeaseRepository
	holderID string
	ttl      time.Duration
}
type TxManager interface {
	Submit(ctx context.Context, req SubmitRequest) (*models.Transaction, error)
	// RefreshStatuses re-checks every non-final transaction's receipt and
	// advances its lifecycle state (pending -> mined -> confirmed, or
	// reverted/reorged). confirmations is the number of blocks required
	// past the receipt's block before a mined tx is considered confirmed.
	RefreshStatuses(ctx context.Context, confirmations uint64) error
	// Replace resubmits a still-pending transaction at the same nonce with a
	// bumped fee. bumpPercent is applied to whichever fee fields the original
	// transaction used. signer MUST be the same signing capability that
	// produced the original transaction (same From address), since the
	// server does not persist private keys — it accepts the same Signer
	// abstraction Submit does (local-keystore/vault/kms-mock backed, not only
	// a raw in-memory key), so this is callable in production configurations,
	// not only tests.
	Replace(ctx context.Context, id string, signer Signer, bumpPercent int64) (*models.Transaction, error)
}

// FeeCaps bounds what a TxManager will ever sign a transaction for: absolute
// gas-price/fee-cap/tip-cap/total-cost limits. A nil field means "no cap"
// for that dimension — buildTx REJECTS (does not
// silently clamp) a transaction that would exceed a configured cap: signing
// at an artificially reduced fee risks a transaction that never confirms,
// which is a worse outcome for a nonce-serialized signer than failing the
// Submit call outright.
type FeeCaps struct {
	// MaxFeePerGas bounds the EIP-1559 fee cap and the legacy gas price alike.
	MaxFeePerGas *big.Int
	// MaxTipPerGas bounds the EIP-1559 tip cap only.
	MaxTipPerGas *big.Int
	// MaxTotalCost bounds gasLimit*feeCap + value, checked regardless of fee mode.
	MaxTotalCost *big.Int
}

// DefaultMaxReplacementAttempts bounds how many times Replace will bump and
// resubmit the SAME nonce before giving up on it automatically and marking
// it as requiring operator intervention. Chosen as a generous-but-finite
// default: an operator manually calling Replace this many times without the
// transaction ever confirming is a strong signal something is wrong beyond
// "the fee was a bit low" (e.g. the account is out of funds, or the RPC
// endpoint itself is unhealthy) and warrants a human looking, not another
// automatic bump.
const DefaultMaxReplacementAttempts = 10

type txManager struct {
	client  Client
	txs     repository.TransactionRepository
	chainID *big.Int
	feeMode FeeMode
	feeCaps FeeCaps
	// maxReplacementAttempts is DefaultMaxReplacementAttempts unless
	// overridden via NewTxManagerWithPolicy; 0 means unlimited (Replace
	// never auto-marks TxNeedsIntervention on attempt count alone).
	maxReplacementAttempts int
	// lease is nil in "in-process" mode (the default — see TxManager's doc
	// comment) and non-nil in "mongo-lease" mode.
	lease *nonceLease

	mu                    sync.Mutex
	keyLocks              map[common.Address]*sync.Mutex
	gasLimitBufferPercent int64
}

// NewTxManager constructs a TxManager backed by client and persisted through
// txs, with no fee caps configured (see NewTxManagerWithFeeCaps).
func NewTxManager(client Client, txs repository.TransactionRepository, chainID *big.Int, feeMode FeeMode) TxManager {
	return NewTxManagerWithFeeCaps(client, txs, chainID, feeMode, FeeCaps{})
}

// NewTxManagerWithFeeCaps is NewTxManager plus absolute fee limits, using
// DefaultMaxReplacementAttempts (see NewTxManagerWithPolicy to override it).
func NewTxManagerWithFeeCaps(client Client, txs repository.TransactionRepository, chainID *big.Int, feeMode FeeMode, feeCaps FeeCaps) TxManager {
	return NewTxManagerWithPolicy(client, txs, chainID, feeMode, feeCaps, DefaultMaxReplacementAttempts)
}

// NewTxManagerWithPolicy is NewTxManagerWithFeeCaps plus an explicit,
// configurable replacement-attempt cap. maxReplacementAttempts<=0 means
// unlimited.
func NewTxManagerWithPolicy(client Client, txs repository.TransactionRepository, chainID *big.Int, feeMode FeeMode, feeCaps FeeCaps, maxReplacementAttempts int) TxManager {
	return &txManager{
		client:                 client,
		txs:                    txs,
		chainID:                chainID,
		feeMode:                feeMode,
		feeCaps:                feeCaps,
		maxReplacementAttempts: maxReplacementAttempts,
		keyLocks:               map[common.Address]*sync.Mutex{},
		gasLimitBufferPercent:  20,
	}
}

// NewTxManagerWithLease is NewTxManagerWithPolicy plus distributed
// multi-replica coordination: the returned
// TxManager acquires a repository.NonceLeaseRepository lease (chainId+":"+
// signerAddress, held for ttl and renewed as needed) before managing a
// signer's nonce sequence in Submit/Replace — see TxManager's "mongo-lease"
// mode doc comment. Every call site sharing the SAME hot key across
// replicas MUST use a distinct, stable-enough-to-debug holderID (a random
// UUID is fine; it need not be human-meaningful, only unique per process)
// and the SAME leases store (config.Config.PersistenceMode=="mongo" — a
// lease with no durable, shared backing store cannot coordinate anything).
func NewTxManagerWithLease(client Client, txs repository.TransactionRepository, chainID *big.Int, feeMode FeeMode, feeCaps FeeCaps, maxReplacementAttempts int, leases repository.NonceLeaseRepository, ttl time.Duration) TxManager {
	m := NewTxManagerWithPolicy(client, txs, chainID, feeMode, feeCaps, maxReplacementAttempts).(*txManager)
	m.lease = &nonceLease{repo: leases, holderID: uuid.NewString(), ttl: ttl}
	return m
}

func (m *txManager) lockFor(addr common.Address) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.keyLocks[addr]
	if !ok {
		l = &sync.Mutex{}
		m.keyLocks[addr] = l
	}
	return l
}

// Submit builds, signs, persists, and broadcasts one transaction.
//
// The following steps run in this exact order, all under the per-signer lock:
//  1. The idempotency lookup — ONCE, here, not repeated pre-lock — is the
//     sole authority on "has this key already been submitted"; a prior
//     pre-lock check let two concurrent callers with the same key both pass
//     it and both broadcast. Only TxFailed and TxReorged records are
//     retryable — every other status, INCLUDING TxBroadcastUnknown, is
//     returned as-is: an ambiguous outcome must never be papered over by
//     allocating a new nonce and signing a second transaction.
//  2. Sign the transaction (so its hash and exact RLP bytes are known).
//  3. Persist the record — with Status TxBroadcastUnknown and the signed
//     bytes in SignedRawTx — BEFORE broadcasting, so an immutable intent
//     record and the exact signed RLP are durable first. A storage failure
//     here means nothing was ever sent: safe to just return
//     the error, no dangling in-flight transaction to reconcile. Create's
//     underlying unique partial index on idempotencyKey (see EnsureIndexes)
//     also closes the last gap: even a same-key race that somehow reached
//     this point concurrently (e.g. across replicas sharing a hot key,
//     which the package doc already documents as unsafe) fails atomically
//     here rather than both broadcasting.
//  4. Broadcast, then resolve the ambiguity immediately if we can:
//     - success: flip to TxPending — we now KNOW it was accepted.
//     - a definite (non-ambiguous) error: flip to TxFailed — nothing
//     reached the mempool, so the idempotency key becomes retryable
//     (see TxFailed's doc comment).
//     - an ambiguous error (timeout, connection reset, ...): LEAVE it
//     TxBroadcastUnknown. RefreshStatuses resolves it later by querying
//     the known hash and, if absent, rebroadcasting the exact same
//     persisted bytes — never a different nonce/signature. If the
//     process crashes before step 4 even runs, the record is durably
//     TxBroadcastUnknown already and RefreshStatuses resolves it the
//     same way after restart.
//
// isRetryableStatus reports whether existing's IdempotencyKey may be reused
// by a fresh Submit call for a new attempt. TxFailed is
// always retryable — nothing ever reached the mempool under that attempt,
// so there is nothing that could still land. TxReorged/
// TxNonceConsumedExternally are retryable ONLY when existing itself was
// marked AllowReorgRetry at Submit time (see SubmitRequest.AllowReorgRetry's
// doc comment) — otherwise a chain-derived state change might still be
// pending (a reorged tx returning to the mempool) or might have already
// happened via an untracked replacement, and blindly resubmitting could
// duplicate the business operation.
func isRetryableStatus(existing *models.Transaction) bool {
	switch existing.Status {
	case models.TxFailed:
		return true
	case models.TxReorged, models.TxNonceConsumedExternally:
		return existing.AllowReorgRetry
	default:
		return false
	}
}

func (m *txManager) Submit(ctx context.Context, req SubmitRequest) (*models.Transaction, error) {
	signer, err := req.signer()
	if err != nil {
		return nil, err
	}

	from, err := signer.Address(ctx)
	if err != nil {
		return nil, fmt.Errorf("blockchain: Signer.Address: %w", err)
	}
	lock := m.lockFor(from)
	lock.Lock()
	defer lock.Unlock()

	// The distributed lease is acquired AFTER the in-process lock (which
	// still serializes concurrent goroutines within this one process —
	// see TxManager's doc comment) but BEFORE anything else touches from's
	// nonce sequence. A no-op returning (0, noop, nil) in "in-process" mode.
	leaseToken, releaseLease, err := m.acquireNonceLease(ctx, from)
	if err != nil {
		return nil, err
	}
	defer releaseLease()

	if blocker, err := m.needsInterventionTx(ctx, from); err != nil {
		return nil, err
	} else if blocker != nil {
		// Refuse to submit behind a stuck nonce, which would prevent later
		// nonce-dependent transactions from being incorrectly reported as
		// finalized. A chain enforces strict nonce ordering, so anything
		// submitted behind a stuck
		// nonce would get stuck too; refuse outright rather than let an
		// operator discover the pile-up transaction by transaction.
		return nil, fmt.Errorf("blockchain: signer %s has transaction %s (nonce %d) requiring operator intervention; resolve it before submitting further transactions", from.Hex(), blocker.ID, blocker.Nonce)
	}

	var existing *models.Transaction
	if req.IdempotencyKey != "" {
		existing, err = m.txs.GetByIdempotencyKey(ctx, req.IdempotencyKey)
		if err != nil {
			if !errors.Is(err, repository.ErrNotFound) {
				return nil, err
			}
			existing = nil
		} else if !isRetryableStatus(existing) {
			return existing, nil // already submitted (or in flight, or ambiguous) under this key
		}
	}

	nonce, err := m.allocateNonce(ctx, from)
	if err != nil {
		return nil, err
	}

	value := req.Value
	if value == nil {
		value = big.NewInt(0)
	}

	unsignedTx, err := m.buildTx(ctx, from, req.To, req.Data, value, nonce)
	if err != nil {
		return nil, err
	}

	signedTx, err := signer.SignTx(ctx, unsignedTx, m.chainID)
	if err != nil {
		return nil, fmt.Errorf("blockchain: sign transaction: %w", err)
	}
	rawHex, err := encodeSignedTx(signedTx)
	if err != nil {
		return nil, err
	}

	// Re-confirm this replica still holds the lease right before the persist
	// step — a no-op success in "in-process" mode. This is the actual REJECT
	// mechanism: a state write whose fencing token is older than the current
	// lease's is refused, so if
	// another replica's Acquire has since taken over (this holder's lease
	// expired), Renew fails and nothing is persisted or broadcast below.
	if err := m.renewNonceLease(ctx, from, leaseToken); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	id := uuid.NewString()
	var priorAttempts []models.TxAttempt
	if existing != nil {
		id = existing.ID // retrying a previously TxFailed/TxReorged key updates the same record rather than orphaning a new one
		// Never overwrite an earlier hash or signed payload silently: we are
		// about to replace existing's Nonce/TxHash/SignedRawTx below, so
		// retain a forensic snapshot of what it was instead of simply
		// discarding it.
		priorAttempts = append(append([]models.TxAttempt{}, existing.Attempts...), models.TxAttempt{
			Nonce: existing.Nonce, TxHash: existing.TxHash, SignedRawTx: existing.SignedRawTx,
			Status: existing.Status, RecordedAt: existing.UpdatedAt,
		})
	}
	record := &models.Transaction{
		ID:              id,
		IdempotencyKey:  req.IdempotencyKey,
		Kind:            req.Kind,
		ChainID:         m.chainID.Int64(),
		From:            from.Hex(),
		To:              req.To.Hex(),
		Data:            "0x" + common.Bytes2Hex(req.Data),
		Value:           value.String(),
		Nonce:           nonce,
		TxHash:          signedTx.Hash().Hex(),
		SignedRawTx:     rawHex,
		Status:          models.TxBroadcastUnknown,
		FencingToken:    leaseToken,
		AllowReorgRetry: req.AllowReorgRetry,
		Attempts:        priorAttempts,
		SubmittedAt:     now,
		UpdatedAt:       now,
	}

	if existing == nil {
		if err := m.txs.Create(ctx, record); err != nil {
			if errors.Is(err, repository.ErrAlreadyExists) {
				if winner, gerr := m.txs.GetByIdempotencyKey(ctx, req.IdempotencyKey); gerr == nil {
					return winner, nil // lost a race to a concurrent Submit under the same key
				}
			}
			return nil, fmt.Errorf("blockchain: persist transaction: %w", err)
		}
	} else if err := m.txs.Update(ctx, record); err != nil {
		return nil, fmt.Errorf("blockchain: persist retried transaction: %w", err)
	}

	if err := m.client.SendTransaction(ctx, signedTx); err != nil {
		if isAmbiguousBroadcast(err) {
			// Already durably TxBroadcastUnknown from the persist above;
			// RefreshStatuses will resolve it. Do not treat this as a
			// definite failure and do not allow a same-key retry.
			return nil, fmt.Errorf("blockchain: SendTransaction (broadcast outcome unknown): %w", err)
		}
		record.Status = models.TxFailed
		record.UpdatedAt = time.Now().UTC()
		_ = m.txs.Update(ctx, record) // best-effort: leaves a retryable TxFailed record under this key instead of a phantom unresolved one
		return nil, fmt.Errorf("blockchain: SendTransaction: %w", err)
	}

	record.Status = models.TxPending
	record.UpdatedAt = time.Now().UTC()
	if err := m.txs.Update(ctx, record); err != nil {
		return nil, fmt.Errorf("blockchain: persist confirmed broadcast: %w", err)
	}
	return record, nil
}

// needsInterventionTx returns from's TxNeedsIntervention record, if any (see
// that status's doc comment on models.TxStatus), so Submit can refuse to
// allocate a nonce behind it.
// ErrLeaseUnavailable is returned by Submit/Replace in "mongo-lease"
// coordination mode when this replica could not acquire, or lost, the
// distributed nonce lease for a signer. It is a fast, retryable failure:
// nothing was allocated, signed, or broadcast, so
// the caller should simply retry (against whichever replica currently
// holds the lease) rather than treat it as a rejection of the underlying
// business operation.
var ErrLeaseUnavailable = errors.New("blockchain: distributed nonce lease unavailable")

// leaseKey is the distributed lease key for from. It always includes at
// least chainId and the signer address.
func (m *txManager) leaseKey(from common.Address) string {
	return m.chainID.String() + ":" + from.Hex()
}

// acquireNonceLease acquires the distributed lease for from in
// "mongo-lease" mode; a no-op that always succeeds with token 0 in
// "in-process" mode (m.lease == nil). release is always non-nil and safe
// to call (a no-op) even when nothing was actually acquired — callers
// should unconditionally `defer release()` right after a successful call.
func (m *txManager) acquireNonceLease(ctx context.Context, from common.Address) (token uint64, release func(), err error) {
	noop := func() {}
	if m.lease == nil {
		return 0, noop, nil
	}
	key := m.leaseKey(from)
	token, ok, err := m.lease.repo.Acquire(ctx, key, m.lease.holderID, m.lease.ttl)
	if err != nil {
		return 0, noop, fmt.Errorf("blockchain: acquire nonce lease for %s: %w", from.Hex(), err)
	}
	if !ok {
		return 0, noop, fmt.Errorf("%w: signer %s's nonce lease is held by another replica", ErrLeaseUnavailable, from.Hex())
	}
	release = func() {
		// Best-effort and deliberately NOT tied to ctx (which may already
		// be Done by the time this runs on an error path) — a caller
		// whose own context was canceled must still release a lease it no
		// longer needs, rather than making every other replica wait out
		// the full TTL for no reason.
		relCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.lease.repo.Release(relCtx, key, m.lease.holderID, token)
	}
	return token, release, nil
}

// renewNonceLease re-confirms this replica still holds from's lease with
// exactly token, right before the persist step of Submit/Replace — a no-op
// success in "in-process" mode. This IS the enforcement mechanism that
// rejects a state write whose fencing token is older than the current
// lease's: if another replica's Acquire has since taken over
// (this holder's lease expired), the underlying Renew call fails and the
// caller must not persist or broadcast anything.
func (m *txManager) renewNonceLease(ctx context.Context, from common.Address, token uint64) error {
	if m.lease == nil {
		return nil
	}
	ok, err := m.lease.repo.Renew(ctx, m.leaseKey(from), m.lease.holderID, token, m.lease.ttl)
	if err != nil {
		return fmt.Errorf("blockchain: renew nonce lease for %s: %w", from.Hex(), err)
	}
	if !ok {
		return fmt.Errorf("%w: signer %s's nonce lease was lost before persisting", ErrLeaseUnavailable, from.Hex())
	}
	return nil
}

func (m *txManager) needsInterventionTx(ctx context.Context, from common.Address) (*models.Transaction, error) {
	stuck, err := m.txs.ListByStatus(ctx, models.TxNeedsIntervention)
	if err != nil {
		return nil, fmt.Errorf("blockchain: list needs-intervention transactions: %w", err)
	}
	for _, tx := range stuck {
		if strings.EqualFold(tx.From, from.Hex()) {
			return tx, nil
		}
	}
	// Treat reorged and externally consumed nonces as NeedsIntervention
	// until the canonical transaction at (chainId, sender, nonce) is
	// identified. A record left TxReorged/
	// TxNonceConsumedExternally with AllowReorgRetry unset is exactly
	// that — its own Kind never opted into "safe to blindly resubmit,"
	// so it blocks this signer the same way an explicit
	// TxNeedsIntervention record does, until an operator resolves it
	// (e.g. by replacing/retrying it with AllowReorgRetry, or otherwise
	// clearing it out of band). A Kind that DID opt in (AllowReorgRetry)
	// is excluded here on purpose — its own reconciler is expected to
	// resubmit it under the same idempotency key shortly (see
	// isRetryableStatus), and blocking the signer in the meantime would
	// defeat that opt-in.
	for _, status := range []models.TxStatus{models.TxReorged, models.TxNonceConsumedExternally} {
		unresolved, err := m.txs.ListByStatus(ctx, status)
		if err != nil {
			return nil, fmt.Errorf("blockchain: list %s transactions: %w", status, err)
		}
		for _, tx := range unresolved {
			if strings.EqualFold(tx.From, from.Hex()) && !tx.AllowReorgRetry {
				return tx, nil
			}
		}
	}
	return nil, nil
}

// allocateNonce reconciles the account's PENDING nonce (mempool-inclusive)
// against its LATEST/mined nonce and this manager's own locally persisted
// records before picking the next nonce to sign, reconciling local state
// against both the "latest" and "pending" transaction counts before
// allocating or recovering nonces.
//
// The three signals disagreeing is exactly the inconsistent-pending-nonce
// failure mode: a lagging/stale RPC node can report a pending nonce BELOW
// the true latest
// (which would produce "nonce too low" if trusted blindly), and this
// manager's own outstanding local records — Submitted but not yet mined —
// are proof of nonces already committed under this key that a node's
// "pending" view might not (yet, or ever, if its mempool view differs)
// reflect. Taking the max of all three never re-uses a nonce (the unsafe
// direction: a collision either double-spends the same operation under two
// hashes or gets the second attempt rejected as underpriced/known), at the
// cost of a false-positive gap if some signal is spuriously inflated — the
// documented, safer-direction trade-off; a persistently inflated "pending"
// reading from a misbehaving node is an operational anomaly RefreshStatuses'
// nonce-consumed-externally check (see handleMissingReceipt) and the
// TxNeedsIntervention path are what ultimately surface to an operator, not
// something Submit's hot path can silently resolve on its own.
func (m *txManager) allocateNonce(ctx context.Context, from common.Address) (uint64, error) {
	pending, err := m.client.PendingNonceAt(ctx, from)
	if err != nil {
		return 0, fmt.Errorf("blockchain: PendingNonceAt: %w", err)
	}
	latest, err := m.client.NonceAt(ctx, from, nil)
	if err != nil {
		return 0, fmt.Errorf("blockchain: NonceAt(latest): %w", err)
	}

	next := pending
	if latest > next {
		next = latest
	}

	all, err := m.txs.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("blockchain: reconcile local nonce state: %w", err)
	}
	for _, tx := range all {
		if !strings.EqualFold(tx.From, from.Hex()) {
			continue
		}
		switch tx.Status {
		case models.TxFailed, models.TxReorged, models.TxNonceConsumedExternally, models.TxReplaced:
			continue // did not/will not consume this nonce going forward
		}
		if candidate := tx.Nonce + 1; candidate > next {
			next = candidate
		}
	}
	return next, nil
}

// buildTx constructs an EIP-1559 or legacy transaction depending on feeMode
// and chain support (falls back to legacy if the latest header has no
// BaseFee, i.e. the chain has not activated EIP-1559).
func (m *txManager) buildTx(ctx context.Context, from, to common.Address, data []byte, value *big.Int, nonce uint64) (*types.Transaction, error) {
	gasLimit, err := m.client.EstimateGas(ctx, ethereum.CallMsg{From: from, To: &to, Data: data, Value: value})
	if err != nil {
		return nil, fmt.Errorf("blockchain: EstimateGas: %w", err)
	}
	gasLimit = gasLimit * (100 + uint64(m.gasLimitBufferPercent)) / 100

	useEIP1559 := m.feeMode == FeeModeEIP1559
	var baseFee *big.Int
	if useEIP1559 {
		header, err := m.client.HeaderByNumber(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("blockchain: HeaderByNumber: %w", err)
		}
		baseFee = header.BaseFee
		if baseFee == nil {
			useEIP1559 = false
		}
	}

	if useEIP1559 {
		tipCap, err := m.client.SuggestGasTipCap(ctx)
		if err != nil {
			return nil, fmt.Errorf("blockchain: SuggestGasTipCap: %w", err)
		}
		feeCap := new(big.Int).Add(new(big.Int).Mul(baseFee, big.NewInt(2)), tipCap)
		if m.feeCaps.MaxTipPerGas != nil && tipCap.Cmp(m.feeCaps.MaxTipPerGas) > 0 {
			return nil, fmt.Errorf("blockchain: suggested tip cap %s exceeds configured MaxTipPerGas %s", tipCap, m.feeCaps.MaxTipPerGas)
		}
		if err := m.checkFeeCaps(feeCap, gasLimit, value); err != nil {
			return nil, err
		}
		return types.NewTx(&types.DynamicFeeTx{
			ChainID:   m.chainID,
			Nonce:     nonce,
			GasTipCap: tipCap,
			GasFeeCap: feeCap,
			Gas:       gasLimit,
			To:        &to,
			Value:     value,
			Data:      data,
		}), nil
	}

	gasPrice, err := m.client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("blockchain: SuggestGasPrice: %w", err)
	}
	if err := m.checkFeeCaps(gasPrice, gasLimit, value); err != nil {
		return nil, err
	}
	return types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      gasLimit,
		To:       &to,
		Value:    value,
		Data:     data,
	}), nil
}

// checkFeeCaps enforces MaxFeePerGas/MaxTotalCost against
// perGasFee — the EIP-1559 fee cap or the legacy gas price, whichever
// buildTx just computed — and the transaction's total cost at that rate.
// Rejects (never silently clamps) — see FeeCaps' doc comment for why.
func (m *txManager) checkFeeCaps(perGasFee *big.Int, gasLimit uint64, value *big.Int) error {
	if m.feeCaps.MaxFeePerGas != nil && perGasFee.Cmp(m.feeCaps.MaxFeePerGas) > 0 {
		return fmt.Errorf("blockchain: computed fee %s per gas exceeds configured MaxFeePerGas %s", perGasFee, m.feeCaps.MaxFeePerGas)
	}
	if m.feeCaps.MaxTotalCost != nil {
		total := new(big.Int).Add(new(big.Int).Mul(perGasFee, new(big.Int).SetUint64(gasLimit)), value)
		if total.Cmp(m.feeCaps.MaxTotalCost) > 0 {
			return fmt.Errorf("blockchain: total cost %s exceeds configured MaxTotalCost %s", total, m.feeCaps.MaxTotalCost)
		}
	}
	return nil
}

// postConfirmationReorgWindow is how many additional blocks past the
// confirmations threshold a Confirmed transaction keeps being actively
// rechecked for a reorg before RefreshStatuses treats it as
// permanently final and stops spending a receipt+header lookup on it every
// tick. Nothing currently threads a per-deployment finality policy through
// TxManager, so this is a fixed, generous default rather than configurable;
// a real deployment wanting a shorter/longer retention window is a
// straightforward follow-up (add a constructor parameter alongside
// FeeCaps), not a correctness gap in the fix itself.
const postConfirmationReorgWindow = 256

// RefreshStatuses re-checks every transaction that is not yet permanently
// final — including already-Confirmed ones, which are still rechecked for a
// reorg for a bounded window rather than skipped forever — and advances or
// corrects its lifecycle state. It also resolves any TxBroadcastUnknown
// record left by Submit or Replace and repairs an interrupted Replace call.
func (m *txManager) RefreshStatuses(ctx context.Context, confirmations uint64) error {
	if err := m.reconcileReplacements(ctx); err != nil {
		return err
	}

	all, err := m.txs.List(ctx)
	if err != nil {
		return err
	}
	currentBlock, err := m.client.BlockNumber(ctx)
	if err != nil {
		return err
	}

	for _, tx := range all {
		// Event-derived records (txindex.ReconcileStackTransactions) are not
		// manager-owned — they carry no signed payload/nonce and their
		// confirmed/reorged status is maintained solely by that projector from
		// indexed events. Skip them so RefreshStatuses never re-queries or
		// rewrites a record it did not submit.
		if models.IsEventDerived(tx) {
			continue
		}
		if m.isFinal(tx, currentBlock, confirmations) {
			continue
		}
		if err := m.refreshOne(ctx, tx, currentBlock, confirmations); err != nil {
			return err
		}
	}
	return nil
}

// isFinal reports whether tx needs no further work from RefreshStatuses.
// TxReplaced/TxFailed are permanently terminal. TxConfirmed becomes final
// once it has sat past confirmations PLUS postConfirmationReorgWindow —
// before that point it is still actively rechecked for a reorg.
func (m *txManager) isFinal(tx *models.Transaction, currentBlock, confirmations uint64) bool {
	switch tx.Status {
	case models.TxReplaced, models.TxFailed:
		return true
	case models.TxConfirmed:
		return currentBlock >= tx.BlockNumber+confirmations+postConfirmationReorgWindow
	default:
		return false
	}
}

// refreshOne re-evaluates a single non-final transaction against its
// current on-chain receipt (if any).
func (m *txManager) refreshOne(ctx context.Context, tx *models.Transaction, currentBlock, confirmations uint64) error {
	receipt, err := m.client.TransactionReceipt(ctx, common.HexToHash(tx.TxHash))
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return m.handleMissingReceipt(ctx, tx)
		}
		return err
	}
	return m.handleReceipt(ctx, tx, receipt, currentBlock, confirmations)
}

// handleMissingReceipt handles a TransactionReceipt lookup that came back
// NotFound.
func (m *txManager) handleMissingReceipt(ctx context.Context, tx *models.Transaction) error {
	switch tx.Status {
	case models.TxBroadcastUnknown, models.TxPending:
		// Before assuming this is simply "still unmined" or resolvable by
		// rebroadcast, check whether the account's on-chain
		// nonce has already passed this transaction's nonce — i.e. SOME
		// transaction, not this one (no receipt was found for its own
		// hash), was mined at this nonce instead. This record can then
		// never confirm no matter how long we wait or how many times we
		// rebroadcast it.
		consumed, err := m.nonceConsumedExternally(ctx, tx)
		if err != nil {
			return err
		}
		if consumed {
			return m.markNonceConsumedExternally(ctx, tx)
		}
		if tx.Status == models.TxBroadcastUnknown {
			// Resolve the ambiguity by rebroadcasting the EXACT SAME
			// persisted bytes — never a new nonce or signature — and
			// let a later tick's receipt lookup decide the outcome.
			return m.rebroadcastUnknown(ctx, tx)
		}
		return nil // TxPending: still simply unmined, nothing to do
	case models.TxMined, models.TxConfirmed, models.TxReverted, models.TxReorged:
		// This record previously had a receipt (it reached Mined/Confirmed/
		// Reverted/Reorged, all of which only ever get set below after a
		// receipt was observed) and now does not: its block — or its
		// inclusion in it — is no longer part of the canonical chain.
		return m.markReorged(ctx, tx)
	default:
		return nil
	}
}

// nonceConsumedExternally reports whether tx.From's on-chain (mined) nonce
// has advanced past tx.Nonce even though no receipt exists for tx's own
// hash — a nonce consumed by an unknown transaction.
func (m *txManager) nonceConsumedExternally(ctx context.Context, tx *models.Transaction) (bool, error) {
	latest, err := m.client.NonceAt(ctx, common.HexToAddress(tx.From), nil)
	if err != nil {
		return false, fmt.Errorf("blockchain: NonceAt(latest) for %s: %w", tx.From, err)
	}
	return latest > tx.Nonce, nil
}

// markNonceConsumedExternally transitions tx to TxNonceConsumedExternally,
// if it is not already there.
func (m *txManager) markNonceConsumedExternally(ctx context.Context, tx *models.Transaction) error {
	if tx.Status == models.TxNonceConsumedExternally {
		return nil
	}
	tx.Status = models.TxNonceConsumedExternally
	tx.UpdatedAt = time.Now().UTC()
	return m.txs.Update(ctx, tx)
}

// rebroadcastUnknown resubmits tx's durably persisted signed bytes
// byte-for-byte. Any error is intentionally ignored here: a definite
// rejection on a rebroadcast most plausibly means the node already knows
// about this exact transaction (e.g. "already known"/"nonce too low"
// because our earlier attempt already landed), not a fresh failure — only a
// receipt (or its continued absence) is trusted to resolve
// TxBroadcastUnknown, never a guess based on this call's error.
func (m *txManager) rebroadcastUnknown(ctx context.Context, tx *models.Transaction) error {
	signedTx, err := decodeSignedTx(tx.SignedRawTx)
	if err != nil {
		return fmt.Errorf("blockchain: rebroadcast %s: %w", tx.ID, err)
	}
	_ = m.client.SendTransaction(ctx, signedTx)
	return nil
}

// handleReceipt advances tx's status from an observed receipt, first
// verifying the receipt's block is still canonical.
func (m *txManager) handleReceipt(ctx context.Context, tx *models.Transaction, receipt *types.Receipt, currentBlock, confirmations uint64) error {
	// A zero BlockHash means the receipt carries no hash to verify against
	// (only ever the case for a hand-built test receipt, never a real
	// node's) — skip the canonicality check rather than treating "no
	// information" as "definitely not canonical".
	if receipt.BlockHash != (common.Hash{}) {
		canonical, err := m.isCanonicalBlock(ctx, receipt.BlockNumber, receipt.BlockHash)
		if err != nil {
			return err
		}
		if !canonical {
			return m.markReorged(ctx, tx)
		}
	}

	newStatus := models.TxMined
	if receipt.Status == types.ReceiptStatusFailed {
		newStatus = models.TxReverted
	} else if currentBlock >= receipt.BlockNumber.Uint64()+confirmations {
		newStatus = models.TxConfirmed
	}

	blockHash := receipt.BlockHash.Hex()
	if newStatus != tx.Status || tx.BlockNumber != receipt.BlockNumber.Uint64() || tx.BlockHash != blockHash {
		tx.Status = newStatus
		tx.BlockNumber = receipt.BlockNumber.Uint64()
		tx.BlockHash = blockHash
		tx.UpdatedAt = time.Now().UTC()
		return m.txs.Update(ctx, tx)
	}
	return nil
}

// isCanonicalBlock reports whether wantHash is still the CURRENT canonical
// block hash at number, by asking the client for whatever header is
// canonical there now — on a live chain this is exactly a reorg check: a
// prior reorg makes HeaderByNumber return a different block (and therefore
// a different hash) at that same height.
func (m *txManager) isCanonicalBlock(ctx context.Context, number *big.Int, wantHash common.Hash) (bool, error) {
	header, err := m.client.HeaderByNumber(ctx, number)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return false, nil // the block itself no longer resolves
		}
		return false, err
	}
	return header.Hash() == wantHash, nil
}

// markReorged transitions tx to TxReorged, if it is not already there.
func (m *txManager) markReorged(ctx context.Context, tx *models.Transaction) error {
	if tx.Status == models.TxReorged {
		return nil
	}
	tx.Status = models.TxReorged
	tx.UpdatedAt = time.Now().UTC()
	return m.txs.Update(ctx, tx)
}

// markNeedsIntervention transitions old to TxNeedsIntervention because it
// has exhausted its configured replacement-attempt limit and returns a
// descriptive error — Replace never signs or broadcasts anything
// for a transaction past this point; see TxNeedsIntervention's doc comment
// for how Submit reacts to it.
func (m *txManager) markNeedsIntervention(ctx context.Context, tx *models.Transaction) error {
	tx.Status = models.TxNeedsIntervention
	tx.UpdatedAt = time.Now().UTC()
	if err := m.txs.Update(ctx, tx); err != nil {
		return fmt.Errorf("blockchain: mark %s needs-intervention: %w", tx.ID, err)
	}
	return fmt.Errorf("blockchain: transaction %s exhausted its replacement-attempt limit (%d); marked for operator intervention", tx.ID, m.maxReplacementAttempts)
}

// reconcileReplacements repairs an interrupted Replace call: if the process
// crashed after persisting a replacement's own record but
// before marking the original transaction Replaced/ReplacedBy, the two
// would otherwise disagree forever — the original would look like it is
// still the live transaction for its nonce while a newer record for that
// same nonce already exists. Every replacement record carries Replaces back
// to its original, so this is self-healing and safe to run every tick.
//
// A replacement record whose OWN Status is TxFailed never actually reached
// the mempool (Replace's SendTransaction failure branch
// already reverts `old` back to TxPending synchronously, in the same
// call), so it must NOT cause this to (re-)mark the original Replaced —
// doing so previously let a stale TxFailed replacement record silently
// undo Replace's own revert on a later tick, marking a still-live original
// Replaced by an attempt that never broadcast anything.
func (m *txManager) reconcileReplacements(ctx context.Context) error {
	all, err := m.txs.List(ctx)
	if err != nil {
		return err
	}
	for _, tx := range all {
		if tx.Replaces == "" || tx.Status == models.TxFailed {
			continue
		}
		original, err := m.txs.Get(ctx, tx.Replaces)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				continue
			}
			return err
		}
		if original.Status == models.TxReplaced {
			continue // already linked
		}
		original.Status = models.TxReplaced
		original.ReplacedBy = tx.ID
		original.UpdatedAt = time.Now().UTC()
		if err := m.txs.Update(ctx, original); err != nil {
			return err
		}
	}
	return nil
}

// Replace resubmits the transaction identified by id at the same nonce
// with a bumped fee. signer MUST be the same signing capability that
// produced the original transaction (same From address). Replace accepts
// the same blockchain.Signer abstraction Submit does, so it works through
// the production local-keystore/vault/kms-mock Signer backends, not only a
// raw *ecdsa.PrivateKey.
//
// Ordering matters: the in-process lock and distributed lease are acquired
// FIRST, from signer's address alone — BEFORE id's record is ever loaded —
// so nothing about `old` is decided from a value that could already be
// stale by the time the lock/lease is actually held. `old` is then loaded
// fresh, and every write to it uses UpdateConditional keyed to the version
// just read, so a stale writer whose lease has since expired and been
// taken over by another replica is rejected AT THE REPOSITORY WRITE
// itself — not only by the earlier Renew call — mirroring the identical
// concern raised for idempotency/nonce-lease records elsewhere in this
// codebase.
//
// The rest of the ordering: build, recheck fee caps against the BUMPED
// values (buildTx only validated the pre-bump fee — a
// bump can push it over a cap the base fee satisfied), sign, then PERSIST
// the replacement intent — the new record (with its signed bytes) AND the
// original's Replaced/ReplacedBy link — before ever calling
// SendTransaction. A crash between persisting the new record and linking
// the original is self-healed by reconcileReplacements (via the new
// record's Replaces field) on the next RefreshStatuses tick — see its doc
// comment. A crash (or ordinary failure) between persisting and
// broadcasting leaves the new record TxBroadcastUnknown, which
// RefreshStatuses also resolves (query the hash; if absent, rebroadcast
// the same bytes).
func (m *txManager) Replace(ctx context.Context, id string, signer Signer, bumpPercent int64) (*models.Transaction, error) {
	if signer == nil {
		return nil, errors.New("blockchain: Replace requires the original signing capability")
	}
	if bumpPercent <= 0 {
		return nil, fmt.Errorf("blockchain: bumpPercent must be positive, got %d", bumpPercent)
	}
	from, err := signer.Address(ctx)
	if err != nil {
		return nil, fmt.Errorf("blockchain: Signer.Address: %w", err)
	}

	lock := m.lockFor(from)
	lock.Lock()
	defer lock.Unlock()

	leaseToken, releaseLease, err := m.acquireNonceLease(ctx, from)
	if err != nil {
		return nil, err
	}
	defer releaseLease()

	// Loaded ONLY after the lock/lease is held — this is the freshest
	// possible read of old's status/replacement-count/version.
	old, err := m.txs.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if old.Status != models.TxPending {
		return nil, fmt.Errorf("blockchain: cannot replace transaction in status %q", old.Status)
	}
	if from.Hex() != old.From {
		return nil, fmt.Errorf("blockchain: replacement signer %s does not match original sender %s", from.Hex(), old.From)
	}
	// If a transaction cannot be replaced within configured limits, mark it
	// as requiring operator intervention. Checked before doing any
	// signing/broadcast work for this attempt.
	if m.maxReplacementAttempts > 0 && old.ReplacementCount+1 > m.maxReplacementAttempts {
		return nil, m.markNeedsIntervention(ctx, old)
	}
	oldVersion := old.Version

	to := common.HexToAddress(old.To)
	data := common.FromHex(old.Data)
	value, ok := new(big.Int).SetString(old.Value, 10)
	if !ok {
		value = big.NewInt(0)
	}

	// Bump from the ORIGINAL signed attempt's own fees, not only a fresh
	// current-network quote — decode it here (best-effort: a
	// record persisted before broadcast always has SignedRawTx, but tolerate
	// its absence by falling back to the pure current-quote bump) so
	// buildBumpedTx can raise from max(originalFee, currentSuggestion) and can
	// never produce a replacement the node rejects as underpriced when
	// current fees have fallen below the original.
	var origTx *types.Transaction
	if old.SignedRawTx != "" {
		if decoded, derr := decodeSignedTx(old.SignedRawTx); derr == nil {
			origTx = decoded
		}
	}
	unsignedTx, err := m.buildBumpedTx(ctx, from, to, data, value, old.Nonce, bumpPercent, origTx)
	if err != nil {
		return nil, err
	}
	signedTx, err := signer.SignTx(ctx, unsignedTx, m.chainID)
	if err != nil {
		return nil, fmt.Errorf("blockchain: sign replacement transaction: %w", err)
	}
	rawHex, err := encodeSignedTx(signedTx)
	if err != nil {
		return nil, err
	}

	// See the identical Submit-side comment.
	if err := m.renewNonceLease(ctx, from, leaseToken); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	newRecord := &models.Transaction{
		ID:               uuid.NewString(),
		Kind:             old.Kind,
		ChainID:          m.chainID.Int64(),
		From:             old.From,
		To:               old.To,
		Data:             old.Data,
		Value:            old.Value,
		Nonce:            old.Nonce,
		TxHash:           signedTx.Hash().Hex(),
		SignedRawTx:      rawHex,
		Replaces:         old.ID,
		ReplacementCount: old.ReplacementCount + 1,
		Status:           models.TxBroadcastUnknown,
		FencingToken:     leaseToken,
		SubmittedAt:      now,
		UpdatedAt:        now,
	}
	if err := m.txs.Create(ctx, newRecord); err != nil {
		return nil, fmt.Errorf("blockchain: persist replacement transaction: %w", err)
	}

	old.Status = models.TxReplaced
	old.ReplacedBy = newRecord.ID
	old.UpdatedAt = now
	if ok, err := m.txs.UpdateConditional(ctx, old, oldVersion); err != nil {
		// newRecord already exists and is real (it must still be
		// broadcast); reconcileReplacements repairs this missing link on
		// the next RefreshStatuses tick via newRecord.Replaces, so this is
		// durable/recoverable rather than fatal enough to unwind newRecord.
		return nil, fmt.Errorf("blockchain: mark original transaction replaced: %w", err)
	} else if !ok {
		// old was modified by someone else (most plausibly
		// another replica's Replace call for the same original, e.g. after
		// this replica's lease was believed expired and taken over)
		// between our read above and this write. Do NOT broadcast
		// newRecord — that would race an unknown concurrent replacement
		// for the same nonce on-chain — abandon this attempt instead.
		failedAt := time.Now().UTC()
		newRecord.Status = models.TxFailed
		newRecord.UpdatedAt = failedAt
		_ = m.txs.Update(ctx, newRecord)
		return nil, fmt.Errorf("blockchain: transaction %s was concurrently modified by another replacement attempt; aborting without broadcasting", id)
	}

	if err := m.client.SendTransaction(ctx, signedTx); err != nil {
		if isAmbiguousBroadcast(err) {
			// Already durably TxBroadcastUnknown; RefreshStatuses resolves it.
			return nil, fmt.Errorf("blockchain: SendTransaction (replacement, outcome unknown): %w", err)
		}
		// Definite rejection: nothing is live on-chain under this
		// replacement. Mark it Failed and give the original its Pending
		// status back (with ReplacedBy cleared) so a future Replace call —
		// not a dead-end Failed replacement — is what gets retried. The
		// attempt still counts against ReplacementCount even though it
		// failed to broadcast (the maximum-replacement-attempts limit bounds
		// attempts made, not just live ones — otherwise a persistently
		// misconfigured bump could retry forever).
		// Conditional on the version this SAME call just wrote above (the
		// TxReplaced transition), so this revert can't clobber a THIRD
		// writer that raced in between.
		failedAt := time.Now().UTC()
		newRecord.Status = models.TxFailed
		newRecord.UpdatedAt = failedAt
		_ = m.txs.Update(ctx, newRecord)
		old.Status = models.TxPending
		old.ReplacedBy = ""
		old.ReplacementCount = newRecord.ReplacementCount
		old.UpdatedAt = failedAt
		if ok, uerr := m.txs.UpdateConditional(ctx, old, oldVersion+1); uerr == nil && !ok {
			// Someone else already moved old past our TxReplaced write —
			// leave it alone rather than overwrite their state; this
			// replacement is Failed either way, which is the important
			// invariant: a FAILED replacement must not let
			// reconcileReplacements mark its original Replaced — see
			// reconcileReplacements' own Status==TxFailed skip, which
			// covers this newRecord regardless of whether this specific
			// revert lands.
		}
		return nil, fmt.Errorf("blockchain: SendTransaction (replacement): %w", err)
	}

	newRecord.Status = models.TxPending
	newRecord.UpdatedAt = time.Now().UTC()
	if err := m.txs.Update(ctx, newRecord); err != nil {
		return nil, fmt.Errorf("blockchain: persist confirmed replacement broadcast: %w", err)
	}
	return newRecord, nil
}

// replacementMinBumpPercent is the minimum fee increase a typical node (geth
// and family) requires over the transaction being replaced before it will
// accept the replacement rather than rejecting it as underpriced. Applied
// as a floor against the ORIGINAL so even a caller-supplied
// bumpPercent below this still yields an acceptable replacement.
const replacementMinBumpPercent = 10

// ceilPercent returns ceil(v * (100+pct) / 100).
func ceilPercent(v *big.Int, pct int64) *big.Int {
	out := new(big.Int).Mul(v, big.NewInt(100+pct))
	out.Add(out, big.NewInt(99))
	return out.Div(out, big.NewInt(100))
}

// bumpFeeField computes one replacement fee field. It bumps bumpPercent
// above the LARGER of the original attempt's own value and the current
// network suggestion, using CEILING arithmetic, then enforces the chain's
// replacement minimum (replacementMinBumpPercent over the original) and
// guarantees the result strictly exceeds the original by at least 1 wei.
// This guards three ways a naive bump can produce a rejected replacement:
// (1) bumping only a fresh current quote could yield a value BELOW the
// original when network fees have since fallen — underpriced; (2) integer
// (floor) division could round a small bump back to exactly the original
// value; and (3) a caller-supplied bumpPercent below the node's replacement
// minimum (e.g. --bump-percent=5) could produce a fee the node still
// rejects. original may be nil (no decodable prior attempt), in which case
// only the current suggestion is bumped.
func bumpFeeField(original, current *big.Int, bumpPercent int64) *big.Int {
	base := new(big.Int)
	if current != nil {
		base.Set(current)
	}
	if original != nil && original.Cmp(base) > 0 {
		base.Set(original)
	}
	bumped := ceilPercent(base, bumpPercent)
	if original != nil {
		// Never below the chain's replacement minimum over the original...
		if minRepl := ceilPercent(original, replacementMinBumpPercent); bumped.Cmp(minRepl) < 0 {
			bumped = minRepl
		}
		// ...and always strictly above the original (covers original==0, where
		// every percentage of 0 is still 0).
		if bumped.Cmp(original) <= 0 {
			bumped = new(big.Int).Add(original, big.NewInt(1))
		}
	}
	return bumped
}

// buildBumpedTx rebuilds a transaction at the same nonce with fees raised by
// bumpPercent, reusing buildTx's fee-mode selection logic, and rechecks
// every configured fee cap against the BUMPED values. buildTx's own check
// inside here only ever saw the pre-bump fee, which can be within cap even
// when the bumped fee is not, so the caps must be re-checked after the bump.
// orig is the decoded original signed attempt (may be nil) so the bump is
// taken from max(original, current) per field.
func (m *txManager) buildBumpedTx(ctx context.Context, from, to common.Address, data []byte, value *big.Int, nonce uint64, bumpPercent int64, orig *types.Transaction) (*types.Transaction, error) {
	base, err := m.buildTx(ctx, from, to, data, value, nonce)
	if err != nil {
		return nil, err
	}
	// Only reuse the original's fee fields when they are of the same fee mode
	// as the transaction we are rebuilding (a dynamic-fee original has no
	// meaningful GasPrice() to compare against a legacy rebuild, and vice
	// versa).
	origSameMode := orig != nil && orig.Type() == base.Type()
	switch base.Type() {
	case types.DynamicFeeTxType:
		var origTip, origFeeCap *big.Int
		if origSameMode {
			origTip = orig.GasTipCap()
			origFeeCap = orig.GasFeeCap()
		}
		tipCap := bumpFeeField(origTip, base.GasTipCap(), bumpPercent)
		feeCap := bumpFeeField(origFeeCap, base.GasFeeCap(), bumpPercent)
		// The fee cap must always cover the tip cap (an EIP-1559 invariant);
		// a fallen base fee could otherwise leave the bumped tip above the
		// bumped fee cap.
		if feeCap.Cmp(tipCap) < 0 {
			feeCap = new(big.Int).Set(tipCap)
		}
		if m.feeCaps.MaxTipPerGas != nil && tipCap.Cmp(m.feeCaps.MaxTipPerGas) > 0 {
			return nil, fmt.Errorf("blockchain: bumped tip cap %s exceeds configured MaxTipPerGas %s", tipCap, m.feeCaps.MaxTipPerGas)
		}
		if err := m.checkFeeCaps(feeCap, base.Gas(), value); err != nil {
			return nil, fmt.Errorf("blockchain: bumped fee rejected: %w", err)
		}
		return types.NewTx(&types.DynamicFeeTx{
			ChainID:   m.chainID,
			Nonce:     nonce,
			GasTipCap: tipCap,
			GasFeeCap: feeCap,
			Gas:       base.Gas(),
			To:        &to,
			Value:     value,
			Data:      data,
		}), nil
	default:
		var origGasPrice *big.Int
		if origSameMode {
			origGasPrice = orig.GasPrice()
		}
		gasPrice := bumpFeeField(origGasPrice, base.GasPrice(), bumpPercent)
		if err := m.checkFeeCaps(gasPrice, base.Gas(), value); err != nil {
			return nil, fmt.Errorf("blockchain: bumped fee rejected: %w", err)
		}
		return types.NewTx(&types.LegacyTx{
			Nonce:    nonce,
			GasPrice: gasPrice,
			Gas:      base.Gas(),
			To:       &to,
			Value:    value,
			Data:     data,
		}), nil
	}
}

var _ TxManager = (*txManager)(nil)
