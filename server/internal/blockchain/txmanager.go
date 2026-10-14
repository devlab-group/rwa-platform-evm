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
// broadcast. Exactly one of PrivateKey or Signer must be set. PrivateKey suits
// local, dev and test use and is adapted through StaticKeySigner; Signer suits
// backends from internal/keys, which keep the key out of process memory.
type SubmitRequest struct {
	IdempotencyKey string
	Kind           string
	PrivateKey     *ecdsa.PrivateKey
	Signer         Signer
	To             common.Address
	Data           []byte
	Value          *big.Int
	// AllowReorgRetry lets this Kind's IdempotencyKey be retried after
	// TxReorged or TxNonceConsumedExternally. Set it only when resubmitting
	// under a new nonce is safe even if the original attempt later lands, for
	// example an idempotent state write such as compliance.StatusService's
	// setStatus. Leave it false for anything that moves value, mints, or has
	// no on-chain replay guard. When false, Submit refuses the retry, and the
	// signer cannot submit further transactions until an operator identifies
	// the canonical transaction at (chainId, sender, nonce) and resolves it.
	AllowReorgRetry bool
}

// signer returns req's Signer, wrapping PrivateKey when that is what the caller
// set
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

// ErrAmbiguousBroadcast may be returned or wrapped by a Client's
// SendTransaction to flag that the outcome of the broadcast is unknown, for
// example when a proxy timed out after forwarding the request to the node.
// isAmbiguousBroadcast also infers this for common transport failures (context
// deadline or cancellation, network errors, unexpected EOF) without the Client
// opting in.
var ErrAmbiguousBroadcast = errors.New("blockchain: ambiguous broadcast outcome")

// isAmbiguousBroadcast reports whether a SendTransaction error leaves the
// outcome unknown. A definite rejection (the node answered with, for example, a
// nonce, underpricing or insufficient-funds error) means nothing reached the
// mempool, so the record becomes TxFailed and its idempotency key can be
// retried. A timeout, connection reset or context deadline may mean the node
// accepted the transaction and only the acknowledgement was lost. Any
// transport-layer failure counts as ambiguous: treating an accepted transaction
// as failed could repeat a privileged operation under a new nonce, while an
// extra TxBroadcastUnknown resolution for a transaction that really failed is
// harmless.
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

// encodeSignedTx returns tx's binary encoding as "0x"-prefixed hex (the EIP-2718
// typed envelope, or plain RLP for a legacy transaction), so the exact signed
// transaction can be persisted before broadcast
func encodeSignedTx(tx *types.Transaction) (string, error) {
	b, err := tx.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("blockchain: encode signed transaction: %w", err)
	}
	return "0x" + common.Bytes2Hex(b), nil
}

// decodeSignedTx reverses encodeSignedTx and returns the transaction TxManager
// signed, so a TxBroadcastUnknown record can be rebroadcast byte for byte
// instead of re-signed, which could change its hash or nonce
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

// nonceLease holds the settings for the distributed nonce lease used in
// "mongo-lease" coordination mode
type nonceLease struct {
	repo     repository.NonceLeaseRepository
	holderID string
	ttl      time.Duration
}

// TxManager serializes nonces per signing key, submits transactions and tracks
// their lifecycle. config.Config.TxCoordinationMode selects:
//   - "in-process" (default): keyLocks serializes goroutines within one manager.
//     Run one platform process per hot key; separate processes can allocate the
//     same pending nonce and replace or drop each other's transactions.
//   - "mongo-lease": Submit and Replace also lease chainId+":"+signerAddress
//     through NonceLeaseRepository across allocation, signing, persistence and
//     broadcast. The in-process lock still serializes local goroutines. Failure
//     to acquire the lease stops before allocation; a failed renewal immediately
//     before persistence stops before saving or broadcasting.
//
// See NewTxManagerWithLease and the operator guide's deployment topology.
type TxManager interface {
	Submit(ctx context.Context, req SubmitRequest) (*models.Transaction, error)
	// RefreshStatuses re-checks every non-final transaction's receipt and
	// advances its lifecycle state (pending, mined, then confirmed, or reverted
	// or reorged). confirmations is the number of blocks that must follow the
	// receipt's block before a mined transaction counts as confirmed.
	RefreshStatuses(ctx context.Context, confirmations uint64) error
	// Replace resubmits a still-pending transaction at the same nonce with a
	// bumped fee. bumpPercent applies to whichever fee fields the original
	// transaction used. signer must be able to sign for the original
	// transaction's From address, because the server does not store private
	// keys. It is the same Signer abstraction that Submit takes.
	Replace(ctx context.Context, id string, signer Signer, bumpPercent int64) (*models.Transaction, error)
}

// FeeCaps are absolute limits on the fees a TxManager will sign. A nil field
// means no cap for that dimension. buildTx rejects a transaction that would
// exceed a cap instead of lowering its fee, because a transaction signed at a
// reduced fee may never confirm, and a stuck nonce is worse for a signer than a
// failed Submit.
type FeeCaps struct {
	// MaxFeePerGas bounds the EIP-1559 fee cap and the legacy gas price alike.
	MaxFeePerGas *big.Int
	// MaxTipPerGas bounds the EIP-1559 tip cap only.
	MaxTipPerGas *big.Int
	// MaxTotalCost bounds gasLimit*feeCap + value, checked regardless of fee mode.
	MaxTotalCost *big.Int
}

// DefaultMaxReplacementAttempts is how many times Replace bumps and resubmits
// the same nonce before it marks the transaction TxNeedsIntervention. A
// transaction that still has not confirmed after this many bumps points to a
// problem beyond a low fee, such as an unfunded account or an unhealthy RPC
// endpoint.
const DefaultMaxReplacementAttempts = 10

type txManager struct {
	client  Client
	txs     repository.TransactionRepository
	chainID *big.Int
	feeMode FeeMode
	feeCaps FeeCaps
	// maxReplacementAttempts defaults to DefaultMaxReplacementAttempts and can be
	// set with NewTxManagerWithPolicy; a value of 0 or less means unlimited, so
	// Replace never marks TxNeedsIntervention on attempt count alone
	maxReplacementAttempts int
	// lease is nil in "in-process" mode (the default) and non-nil in
	// "mongo-lease" mode
	lease *nonceLease

	mu                    sync.Mutex
	keyLocks              map[common.Address]*sync.Mutex
	gasLimitBufferPercent int64
}

// NewTxManager constructs a TxManager that uses client and stores records in
// txs, with no fee caps (see NewTxManagerWithFeeCaps)
func NewTxManager(client Client, txs repository.TransactionRepository, chainID *big.Int, feeMode FeeMode) TxManager {
	return NewTxManagerWithFeeCaps(client, txs, chainID, feeMode, FeeCaps{})
}

// NewTxManagerWithFeeCaps is NewTxManager with fee limits and
// DefaultMaxReplacementAttempts (see NewTxManagerWithPolicy to change it)
func NewTxManagerWithFeeCaps(client Client, txs repository.TransactionRepository, chainID *big.Int, feeMode FeeMode, feeCaps FeeCaps) TxManager {
	return NewTxManagerWithPolicy(client, txs, chainID, feeMode, feeCaps, DefaultMaxReplacementAttempts)
}

// NewTxManagerWithPolicy is NewTxManagerWithFeeCaps with an explicit
// replacement-attempt limit. maxReplacementAttempts <= 0 means unlimited
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

// NewTxManagerWithLease is NewTxManagerWithPolicy with the distributed nonce
// lease of "mongo-lease" mode (see TxManager). The lease key is
// chainId+":"+signerAddress, held for ttl. Each process gets a random holder ID.
// Replicas that share a hot key must share the same leases store, which needs
// durable shared storage (config PersistenceMode "mongo"), or the lease cannot
// coordinate them.
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

// isRetryableStatus reports whether existing's IdempotencyKey can be reused by
// a new Submit. TxFailed always is: nothing reached the mempool under that
// attempt, so nothing can still land. TxReorged and TxNonceConsumedExternally
// are retryable only when existing was submitted with AllowReorgRetry (see
// SubmitRequest.AllowReorgRetry). Otherwise a reorged transaction may return to
// the mempool, or the nonce may have been used by an untracked replacement, and
// resubmitting could repeat the business operation.
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

// Submit builds, signs, persists and broadcasts under the per-signer lock and,
// in mongo-lease mode, a distributed lease:
//  1. Reject a signer blocked by needsInterventionTx.
//  2. Look up the idempotency key under the lock. Retry only isRetryableStatus
//     records; return others unchanged. TxBroadcastUnknown must retain its nonce
//     and signature rather than start a second operation.
//  3. Allocate a nonce, build and sign, fixing the hash and signed bytes.
//  4. Persist TxBroadcastUnknown with SignedRawTx before broadcast. A storage
//     failure sends nothing; the unique partial idempotencyKey index blocks a
//     same-key race at persistence.
//  5. Broadcast: success becomes TxPending; definite rejection becomes TxFailed
//     and permits retry; an ambiguous error retains TxBroadcastUnknown.
//
// RefreshStatuses resolves unknown outcomes by hash and, if absent, rebroadcasts
// the persisted bytes with the same nonce and signature. This also recovers a
// crash between persistence and broadcast.
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

	// the lease is taken after the in-process lock, which still serializes
	// goroutines within this process, and before anything touches from's nonce
	// sequence; it does nothing in "in-process" mode
	leaseToken, releaseLease, err := m.acquireNonceLease(ctx, from)
	if err != nil {
		return nil, err
	}
	defer releaseLease()

	if blocker, err := m.needsInterventionTx(ctx, from); err != nil {
		return nil, err
	} else if blocker != nil {
		// nodes mine a signer's transactions in nonce order, so anything
		// submitted behind a stuck nonce would be stuck too
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

	// Confirm this replica still holds the lease right before the persist step
	// (a no-op in "in-process" mode). If another replica has taken over since
	// this lease expired, Renew fails and nothing is persisted or broadcast
	if err := m.renewNonceLease(ctx, from, leaseToken); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	id := uuid.NewString()
	var priorAttempts []models.TxAttempt
	if existing != nil {
		id = existing.ID // a retry updates the same record instead of creating a second one for the key
		// the record's Nonce, TxHash and SignedRawTx are about to be replaced, so
		// keep the earlier attempt in Attempts
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
			// the record is already TxBroadcastUnknown from the persist above and
			// RefreshStatuses resolves it; this is not a definite failure, so a
			// same-key retry stays blocked
			return nil, fmt.Errorf("blockchain: SendTransaction (broadcast outcome unknown): %w", err)
		}
		record.Status = models.TxFailed
		record.UpdatedAt = time.Now().UTC()
		_ = m.txs.Update(ctx, record) // best effort: a retryable TxFailed record under this key beats a stale TxBroadcastUnknown one
		return nil, fmt.Errorf("blockchain: SendTransaction: %w", err)
	}

	record.Status = models.TxPending
	record.UpdatedAt = time.Now().UTC()
	if err := m.txs.Update(ctx, record); err != nil {
		return nil, fmt.Errorf("blockchain: persist confirmed broadcast: %w", err)
	}
	return record, nil
}

// ErrLeaseUnavailable is returned by Submit and Replace in "mongo-lease" mode
// when this replica could not acquire the signer's distributed nonce lease or
// lost it. Nothing was allocated, signed or broadcast, so the caller can retry
// instead of treating it as a rejection of the business operation.
var ErrLeaseUnavailable = errors.New("blockchain: distributed nonce lease unavailable")

// leaseKey returns the distributed lease key for from, chainId:address
func (m *txManager) leaseKey(from common.Address) string {
	return m.chainID.String() + ":" + from.Hex()
}

// acquireNonceLease acquires the distributed lease for from in "mongo-lease"
// mode. In "in-process" mode (m.lease == nil) it succeeds with token 0 and does
// nothing. release is never nil and is safe to call even when nothing was
// acquired, so callers can `defer release()` after a successful call.
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
		// best effort, and not tied to ctx, which may already be done on an
		// error path: a caller whose context was canceled should still release
		// the lease instead of making other replicas wait out the TTL
		relCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.lease.repo.Release(relCtx, key, m.lease.holderID, token)
	}
	return token, release, nil
}

// renewNonceLease confirms that this replica still holds from's lease with
// exactly token, just before the persist step of Submit and Replace. It does
// nothing in "in-process" mode. If another replica has taken over since this
// lease expired, Renew fails and the caller must not persist or broadcast
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

// needsInterventionTx returns a transaction that blocks from from submitting,
// or nil. That is a TxNeedsIntervention record, or a TxReorged or
// TxNonceConsumedExternally record whose Kind did not set AllowReorgRetry: its
// canonical transaction at (chainId, sender, nonce) has not been identified, so
// it blocks the signer until an operator resolves it. A record with
// AllowReorgRetry is not a blocker, since its reconciler is expected to resubmit
// it under the same idempotency key (see isRetryableStatus).
func (m *txManager) needsInterventionTx(ctx context.Context, from common.Address) (*models.Transaction, error) {
	stuck, err := m.txs.ListByStatuses(ctx, models.TxNeedsIntervention, models.TxReorged, models.TxNonceConsumedExternally)
	if err != nil {
		return nil, fmt.Errorf("blockchain: list blocking transactions: %w", err)
	}
	for _, tx := range stuck {
		if tx.Status == models.TxNeedsIntervention && strings.EqualFold(tx.From, from.Hex()) {
			return tx, nil
		}
	}
	for _, tx := range stuck {
		switch tx.Status {
		case models.TxReorged, models.TxNonceConsumedExternally:
			if strings.EqualFold(tx.From, from.Hex()) && !tx.AllowReorgRetry {
				return tx, nil
			}
		}
	}
	return nil, nil
}

// allocateNonce returns the next nonce to sign with: the maximum of the node's
// pending nonce (eth_getTransactionCount at "pending", which counts the node's
// pending transactions), its latest nonce (mined transactions only), and one
// past the highest nonce among this manager's own records that still hold their
// nonce.
//
// The three can disagree. A lagging node can report a pending nonce below the
// latest one, which would give "nonce too low" if trusted, and this manager's
// own broadcast but unmined records commit nonces that a node's pending view
// may not show. Taking the maximum never reuses a nonce. Reuse would either
// replace the earlier transaction (when the fee is bumped enough) or be
// rejected by the node as underpriced or already known. The cost is a possible
// gap in the sequence when a signal is inflated. A persistently inflated
// pending count from a misbehaving node is not something Submit can resolve; it
// surfaces through RefreshStatuses' nonce-consumed-externally check (see
// handleMissingReceipt) and the TxNeedsIntervention path.
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
			continue // these no longer hold this nonce
		}
		if candidate := tx.Nonce + 1; candidate > next {
			next = candidate
		}
	}
	return next, nil
}

// buildTx builds an unsigned EIP-1559 (type 2) or legacy transaction according
// to feeMode, falling back to legacy when the latest header has no BaseFee (the
// chain has not activated EIP-1559). The gas limit is the estimate plus
// gasLimitBufferPercent. An EIP-1559 fee cap is twice the latest base fee plus
// the suggested tip.
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

// checkFeeCaps checks MaxFeePerGas against perGasFee (the EIP-1559 fee cap or
// the legacy gas price) and MaxTotalCost against gasLimit*perGasFee + value. It
// returns an error instead of lowering the fee (see FeeCaps).
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

// postConfirmationReorgWindow is how many blocks past the confirmations
// threshold RefreshStatuses keeps rechecking a Confirmed transaction for a
// reorg. After that the transaction is final and costs no further receipt and
// header lookups. The window is fixed, not configurable.
const postConfirmationReorgWindow = 256

// RefreshStatuses re-checks every transaction that is not yet final, including
// Confirmed ones still inside the reorg window, and advances or corrects its
// status. It also resolves TxBroadcastUnknown records left by Submit or Replace
// and repairs an interrupted Replace.
func (m *txManager) RefreshStatuses(ctx context.Context, confirmations uint64) error {
	if err := m.reconcileReplacements(ctx); err != nil {
		return err
	}

	currentBlock, err := m.client.BlockNumber(ctx)
	if err != nil {
		return err
	}
	// ask the repository only for records isFinal would keep, so old finalized
	// records are not read back; the bound is 0 on a young chain, and isFinal
	// still decides each record
	var confirmedFrom uint64
	if cutoff := confirmations + postConfirmationReorgWindow; currentBlock > cutoff {
		confirmedFrom = currentBlock - cutoff
	}
	all, err := m.txs.ListUnfinalized(ctx, confirmedFrom)
	if err != nil {
		return err
	}

	for _, tx := range all {
		// event-derived records (txindex.ReconcileStackTransactions) belong to
		// that projector: they have no signed payload or nonce, and it alone
		// maintains their status from indexed events
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
// TxReplaced and TxFailed are terminal. TxConfirmed becomes final once the chain
// is confirmations plus postConfirmationReorgWindow blocks past its block.
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

// refreshOne re-evaluates one non-final transaction against its receipt, if it
// has one
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

// handleMissingReceipt handles a receipt lookup that returned NotFound
func (m *txManager) handleMissingReceipt(ctx context.Context, tx *models.Transaction) error {
	switch tx.Status {
	case models.TxBroadcastUnknown, models.TxPending:
		// if the account's mined nonce has passed this transaction's nonce
		// while no receipt exists for its own hash, a different transaction
		// took the nonce, and this one can never confirm
		consumed, err := m.nonceConsumedExternally(ctx, tx)
		if err != nil {
			return err
		}
		if consumed {
			return m.markNonceConsumedExternally(ctx, tx)
		}
		if tx.Status == models.TxBroadcastUnknown {
			// rebroadcast the persisted bytes (never a new nonce or signature)
			// and let a later tick's receipt lookup decide the outcome
			return m.rebroadcastUnknown(ctx, tx)
		}
		return nil // TxPending: still simply unmined, nothing to do
	case models.TxMined, models.TxConfirmed, models.TxReverted, models.TxReorged:
		// these statuses are set only after a receipt was observed, so the
		// receipt has disappeared: its block, or its inclusion in it, is no
		// longer on the canonical chain
		return m.markReorged(ctx, tx)
	default:
		return nil
	}
}

// nonceConsumedExternally reports whether tx.From's mined nonce has passed
// tx.Nonce although no receipt exists for tx's own hash, meaning an unknown
// transaction used the nonce
func (m *txManager) nonceConsumedExternally(ctx context.Context, tx *models.Transaction) (bool, error) {
	latest, err := m.client.NonceAt(ctx, common.HexToAddress(tx.From), nil)
	if err != nil {
		return false, fmt.Errorf("blockchain: NonceAt(latest) for %s: %w", tx.From, err)
	}
	return latest > tx.Nonce, nil
}

// markNonceConsumedExternally sets tx to TxNonceConsumedExternally unless it
// already is
func (m *txManager) markNonceConsumedExternally(ctx context.Context, tx *models.Transaction) error {
	if tx.Status == models.TxNonceConsumedExternally {
		return nil
	}
	tx.Status = models.TxNonceConsumedExternally
	tx.UpdatedAt = time.Now().UTC()
	return m.txs.Update(ctx, tx)
}

// rebroadcastUnknown resubmits tx's persisted signed bytes unchanged. It ignores
// the SendTransaction error: a rejection most likely means the node already has
// the transaction (for example "already known" or "nonce too low" because the
// earlier attempt landed). Only a receipt, or its continued absence, resolves
// TxBroadcastUnknown.
func (m *txManager) rebroadcastUnknown(ctx context.Context, tx *models.Transaction) error {
	signedTx, err := decodeSignedTx(tx.SignedRawTx)
	if err != nil {
		return fmt.Errorf("blockchain: rebroadcast %s: %w", tx.ID, err)
	}
	_ = m.client.SendTransaction(ctx, signedTx)
	return nil
}

// handleReceipt advances tx's status from a receipt, after checking that the
// receipt's block is still canonical
func (m *txManager) handleReceipt(ctx context.Context, tx *models.Transaction, receipt *types.Receipt, currentBlock, confirmations uint64) error {
	// a zero BlockHash means there is nothing to verify against (only a
	// hand-built test receipt lacks one), so skip the canonical check
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

// isCanonicalBlock reports whether wantHash is the hash of the header the node
// currently returns at number. After a reorg the node returns a different
// block, with a different hash, at the same height.
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

// markReorged sets tx to TxReorged unless it already is
func (m *txManager) markReorged(ctx context.Context, tx *models.Transaction) error {
	if tx.Status == models.TxReorged {
		return nil
	}
	tx.Status = models.TxReorged
	tx.UpdatedAt = time.Now().UTC()
	return m.txs.Update(ctx, tx)
}

// markNeedsIntervention sets tx to TxNeedsIntervention because it used up its
// replacement-attempt limit, and returns an error saying so. Replace signs and
// broadcasts nothing in that case; see needsInterventionTx for how Submit
// reacts.
func (m *txManager) markNeedsIntervention(ctx context.Context, tx *models.Transaction) error {
	tx.Status = models.TxNeedsIntervention
	tx.UpdatedAt = time.Now().UTC()
	if err := m.txs.Update(ctx, tx); err != nil {
		return fmt.Errorf("blockchain: mark %s needs-intervention: %w", tx.ID, err)
	}
	return fmt.Errorf("blockchain: transaction %s exhausted its replacement-attempt limit (%d); marked for operator intervention", tx.ID, m.maxReplacementAttempts)
}

// reconcileReplacements repairs an interrupted Replace. If the process crashed
// after persisting the replacement record but before marking the original
// Replaced, the original would look like the live transaction for its nonce
// while a newer record for the same nonce exists. Every replacement record
// carries Replaces, so the repair needs no other state and is safe to run on
// every tick.
//
// A replacement whose own Status is TxFailed never reached the mempool, and
// Replace has already reverted the original to TxPending in the same call.
// Such a record is skipped, so it cannot mark a live original Replaced.
func (m *txManager) reconcileReplacements(ctx context.Context) error {
	all, err := m.txs.ListReplacements(ctx)
	if err != nil {
		return err
	}
	for _, tx := range all {
		if tx.Status == models.TxFailed {
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

// Replace resubmits id at the same nonce with a bumped fee. signer may use any
// backend but must control the original From address.
//
// Acquire the signer's in-process lock and distributed lease before loading the
// record. Updates to the original use UpdateConditional with its read version,
// rejecting a stale writer even if an earlier lease renewal succeeded.
//
// Build and sign the replacement, checking fee caps after the bump. Persist its
// signed bytes and the original's Replaced/ReplacedBy link before SendTransaction.
// If a crash interrupts linking, reconcileReplacements repairs it during
// RefreshStatuses. An interrupted or ambiguous broadcast leaves TxBroadcastUnknown;
// refresh looks up the hash and rebroadcasts the same bytes if absent.
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

	// loaded only after the lock and lease are held, so its status,
	// replacement count and version are current
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
	// past the replacement-attempt limit the transaction needs an operator;
	// checked before any signing or broadcasting for this attempt
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

	// decode the original signed attempt so buildBumpedTx can bump from the
	// larger of its fees and the current suggestion, which keeps the replacement
	// from being underpriced after fees fall; without SignedRawTx it bumps the
	// current suggestion alone
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

	// confirm the lease is still held, as in Submit
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
		// newRecord is already saved and still has to be broadcast;
		// reconcileReplacements restores the missing link on the next
		// RefreshStatuses run through newRecord.Replaces
		return nil, fmt.Errorf("blockchain: mark original transaction replaced: %w", err)
	} else if !ok {
		// someone else changed old between our read and this write, most likely
		// another replica's Replace for the same original after this lease
		// expired. Do not broadcast newRecord, which would race that replacement
		// for the same nonce; abandon the attempt
		failedAt := time.Now().UTC()
		newRecord.Status = models.TxFailed
		newRecord.UpdatedAt = failedAt
		_ = m.txs.Update(ctx, newRecord)
		return nil, fmt.Errorf("blockchain: transaction %s was concurrently modified by another replacement attempt; aborting without broadcasting", id)
	}

	if err := m.client.SendTransaction(ctx, signedTx); err != nil {
		if isAmbiguousBroadcast(err) {
			// the record is already TxBroadcastUnknown and RefreshStatuses resolves it
			return nil, fmt.Errorf("blockchain: SendTransaction (replacement, outcome unknown): %w", err)
		}
		// a definite rejection: nothing is live under this replacement. Mark it
		// Failed and restore the original to Pending with ReplacedBy cleared, so
		// a later Replace retries the original. The attempt still counts toward
		// ReplacementCount, because the limit bounds attempts made and a
		// misconfigured bump would otherwise retry forever. The revert is
		// conditional on the version this call just wrote, so it cannot overwrite
		// a third writer.
		failedAt := time.Now().UTC()
		newRecord.Status = models.TxFailed
		newRecord.UpdatedAt = failedAt
		_ = m.txs.Update(ctx, newRecord)
		old.Status = models.TxPending
		old.ReplacedBy = ""
		old.ReplacementCount = newRecord.ReplacementCount
		old.UpdatedAt = failedAt
		if ok, uerr := m.txs.UpdateConditional(ctx, old, oldVersion+1); uerr == nil && !ok {
			// someone else already moved old past our TxReplaced write, so leave
			// their state alone; reconcileReplacements skips the Failed
			// replacement whether or not this revert lands
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

// replacementMinBumpPercent is the fee increase over the replaced transaction
// that geth requires by default (--txpool.pricebump 10) before it accepts a
// replacement; below that it rejects it as underpriced. Geth applies it to both
// the fee cap and the tip cap. bumpFeeField uses it as a floor, so a smaller
// caller-supplied bumpPercent still yields an acceptable replacement on a node
// with the default.
const replacementMinBumpPercent = 10

// ceilPercent returns ceil(v * (100+pct) / 100).
func ceilPercent(v *big.Int, pct int64) *big.Int {
	out := new(big.Int).Mul(v, big.NewInt(100+pct))
	out.Add(out, big.NewInt(99))
	return out.Div(out, big.NewInt(100))
}

// bumpFeeField computes one replacement fee field. It raises the larger of the
// original attempt's value and the current network suggestion by bumpPercent,
// rounding up, then enforces replacementMinBumpPercent over the original and
// makes the result exceed the original by at least 1 wei. That avoids three
// ways a plain bump yields a rejected replacement: bumping only a fresh quote
// can land below the original after fees fall, rounding down can return the
// original value, and a caller-supplied bumpPercent under the node's minimum
// (for example 5) is still rejected. original may be nil when there is no
// decodable earlier attempt, in which case only the current suggestion is
// bumped.
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
		// never below the replacement minimum over the original
		if minRepl := ceilPercent(original, replacementMinBumpPercent); bumped.Cmp(minRepl) < 0 {
			bumped = minRepl
		}
		// and always above the original, which also covers original == 0 (any
		// percentage of 0 is 0)
		if bumped.Cmp(original) <= 0 {
			bumped = new(big.Int).Add(original, big.NewInt(1))
		}
	}
	return bumped
}

// buildBumpedTx rebuilds a transaction at the same nonce with fees raised by
// bumpPercent, using buildTx's fee-mode selection, and rechecks the fee caps
// against the bumped values, since buildTx saw only the unbumped fee. orig is
// the decoded original signed attempt (may be nil); each fee field is bumped
// from the larger of the original and current values.
func (m *txManager) buildBumpedTx(ctx context.Context, from, to common.Address, data []byte, value *big.Int, nonce uint64, bumpPercent int64, orig *types.Transaction) (*types.Transaction, error) {
	base, err := m.buildTx(ctx, from, to, data, value, nonce)
	if err != nil {
		return nil, err
	}
	// use the original's fees only when it has the same transaction type as the
	// rebuild, because a dynamic-fee original has no legacy gas price to compare
	// with, and the reverse
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
		// EIP-1559 requires the fee cap to be at least the tip cap, and a fallen
		// base fee could leave the bumped tip above the bumped fee cap
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
