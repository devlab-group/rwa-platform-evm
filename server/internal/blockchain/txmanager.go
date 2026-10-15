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

// SubmitRequest describes a transaction to sign and broadcast. Supply Signer or a
// local/dev/test PrivateKey, adapted through StaticKeySigner. Signer takes precedence
// when both are set; internal/keys provides local and remote backends.
type SubmitRequest struct {
	IdempotencyKey string
	Kind           string
	PrivateKey     *ecdsa.PrivateKey
	Signer         Signer
	To             common.Address
	Data           []byte
	Value          *big.Int
	// AllowReorgRetry permits same-key retries after TxReorged or
	// TxNonceConsumedExternally. Enable only when a new-nonce retry is safe even if the
	// original later lands, such as an idempotent setStatus. Leave false for value
	// transfers, mints or operations without replay protection: the signer stays blocked
	// until an operator identifies and resolves the canonical (chainId, sender, nonce)
	// transaction.
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

// ErrAmbiguousBroadcast lets Client.SendTransaction signal an unknown outcome, such as
// a proxy timeout after forwarding. isAmbiguousBroadcast also recognizes common
// transport failures.
var ErrAmbiguousBroadcast = errors.New("blockchain: ambiguous broadcast outcome")

// isAmbiguousBroadcast classifies ErrAmbiguousBroadcast, context
// cancellation/deadlines, net.Error and EOF errors as unknown outcomes: the node may
// have accepted the transaction before losing acknowledgement. Other errors are treated
// as definite rejections and become retryable TxFailed records. Ambiguous outcomes
// retain TxBroadcastUnknown to avoid repeating an accepted operation under a new nonce.
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

// encodeSignedTx returns 0x-prefixed transaction bytes for persistence before
// broadcast: an EIP-2718 envelope or legacy RLP
func encodeSignedTx(tx *types.Transaction) (string, error) {
	b, err := tx.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("blockchain: encode signed transaction: %w", err)
	}
	return "0x" + common.Bytes2Hex(b), nil
}

// decodeSignedTx reverses encodeSignedTx for byte-identical rebroadcast of
// TxBroadcastUnknown, preserving hash, nonce and signature
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

// TxManager serializes nonces per signer and tracks transactions.
// config.Config.TxCoordinationMode selects:
//   - "in-process" (default): local keyLocks only. Run one process per hot key; separate
//     processes can collide on pending nonces and replace or drop each other's
//     transactions.
//   - "mongo-lease": Submit and Replace also hold a NonceLeaseRepository lease on
//     chainId+":"+signerAddress across allocation, signing, persistence and broadcast.
//     Acquisition failure stops allocation; pre-persistence renewal failure stops saving
//     and broadcasting. Local locking remains.
//
// See NewTxManagerWithLease and the operator guide's deployment topology.
type TxManager interface {
	Submit(ctx context.Context, req SubmitRequest) (*models.Transaction, error)
	// RefreshStatuses reconciles non-final transactions against receipts and chain state.
	// confirmations counts blocks after the receipt's block.
	RefreshStatuses(ctx context.Context, confirmations uint64) error
	// Replace resubmits a Pending transaction at its nonce with fees raised by
	// bumpPercent. Its Signer must control the original From address; buildBumpedTx
	// details fee-mode handling.
	Replace(ctx context.Context, id string, signer Signer, bumpPercent int64) (*models.Transaction, error)
}

// FeeCaps limits transaction signing costs; nil disables a dimension. buildTx rejects
// excessive fees rather than lowering them and risking a stuck nonce.
type FeeCaps struct {
	// MaxFeePerGas bounds the EIP-1559 fee cap and the legacy gas price alike.
	MaxFeePerGas *big.Int
	// MaxTipPerGas bounds the EIP-1559 tip cap only.
	MaxTipPerGas *big.Int
	// MaxTotalCost bounds gasLimit*feeCap + value, checked regardless of fee mode.
	MaxTotalCost *big.Int
}

// DefaultMaxReplacementAttempts caps same-nonce replacement attempts before
// TxNeedsIntervention. Repeated failure may indicate an unfunded account or unhealthy
// RPC endpoint rather than a low fee.
const DefaultMaxReplacementAttempts = 10

type txManager struct {
	client  Client
	txs     repository.TransactionRepository
	chainID *big.Int
	feeMode FeeMode
	feeCaps FeeCaps
	// maxReplacementAttempts defaults to DefaultMaxReplacementAttempts;
	// NewTxManagerWithPolicy overrides it. Non-positive values disable attempt-count
	// intervention.
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

// NewTxManagerWithLease adds mongo-lease coordination to NewTxManagerWithPolicy: a
// random holder ID and a TTL lease on chainId+":"+signerAddress. Replicas sharing a hot
// key must use the same durable lease store, configured with PersistenceMode="mongo".
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

// isRetryableStatus permits same-key Submit retries for TxFailed. TxReorged and
// TxNonceConsumedExternally require the stored AllowReorgRetry flag: otherwise an
// original or untracked replacement could still perform the operation. See
// SubmitRequest.AllowReorgRetry.
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

// Submit holds the per-signer lock and optional distributed lease. It checks
// needsInterventionTx, returns non-retryable idempotency records unchanged, then
// allocates a nonce, builds/signs and persists TxBroadcastUnknown with SignedRawTx
// before broadcast. Persistence failure sends nothing; repository uniqueness arbitrates
// same-key creates.
// Successful broadcast becomes TxPending; classified rejection becomes retryable
// TxFailed; ambiguous errors retain TxBroadcastUnknown. RefreshStatuses checks the hash
// and, while the nonce remains available, rebroadcasts identical bytes, recovering
// persistence-to-broadcast crashes.
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

	// Acquire the optional distributed lease after the local lock, before accessing the
	// signer's nonce sequence
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

	// Renew before persistence; a lost lease must stop saving and broadcasting. In-process
	// mode is a no-op.
	if err := m.renewNonceLease(ctx, from, leaseToken); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	id := uuid.NewString()
	var priorAttempts []models.TxAttempt
	if existing != nil {
		id = existing.ID // a retry updates the same record instead of creating a second one for the key
		// Archive the earlier Nonce, TxHash and SignedRawTx in Attempts before replacing them
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
			// Retain TxBroadcastUnknown for RefreshStatuses; same-key Submit must not start
			// another operation
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

// ErrLeaseUnavailable signals a held or lost mongo-lease. Acquisition failure precedes
// allocation; renewal failure may follow signing but stops persistence and broadcast.
// Callers may retry the coordination failure.
var ErrLeaseUnavailable = errors.New("blockchain: distributed nonce lease unavailable")

// leaseKey returns the distributed lease key for from, chainId:address
func (m *txManager) leaseKey(from common.Address) string {
	return m.chainID.String() + ":" + from.Hex()
}

// acquireNonceLease obtains from's distributed lease. In-process mode returns token 0
// without acquiring anything. release is always callable, including after failed
// acquisition.
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
		// Release best-effort on a fresh context so caller cancellation does not leave
		// replicas waiting for TTL expiry
		relCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.lease.repo.Release(relCtx, key, m.lease.holderID, token)
	}
	return token, release, nil
}

// renewNonceLease checks the holder and token before Submit/Replace persistence.
// Failure must stop saving and broadcasting; in-process mode is a no-op.
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

// needsInterventionTx returns a signer blocker or nil. TxNeedsIntervention always
// blocks; TxReorged and TxNonceConsumedExternally block unless AllowReorgRetry permits
// same-key recovery. Otherwise an operator must identify and resolve the canonical
// (chainId, sender, nonce) transaction.
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

// allocateNonce takes the maximum of node pending/latest nonces and one past the
// highest stored nonce still held for the signer. This handles lagging pending views
// and local broadcasts absent from the node's mempool view, avoiding collisions with
// those records.
// Inflated signals can leave nonce gaps that Submit cannot repair. Reusing a held nonce
// could replace the earlier transaction or trigger underpriced/already-known rejection;
// external consumption and stuck transactions are handled separately by RefreshStatuses
// and operator intervention.
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

// buildTx builds unsigned EIP-1559 or legacy transactions, falling back to legacy if
// BaseFee is absent. Gas uses the estimate plus gasLimitBufferPercent; the EIP-1559 fee
// cap is twice BaseFee plus the suggested tip.
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

// checkFeeCaps rejects perGasFee above MaxFeePerGas or gasLimit*perGasFee+value above
// MaxTotalCost. perGasFee is the EIP-1559 cap or legacy gas price; see FeeCaps.
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

// postConfirmationReorgWindow fixes the extra blocks after confirmation during which
// RefreshStatuses checks Confirmed transactions for reorgs. Final records need no
// further receipt/header lookups.
const postConfirmationReorgWindow = 256

// RefreshStatuses repairs replacement links, resolves TxBroadcastUnknown from
// Submit/Replace and updates non-final records, including Confirmed transactions within
// the reorg window
func (m *txManager) RefreshStatuses(ctx context.Context, confirmations uint64) error {
	if err := m.reconcileReplacements(ctx); err != nil {
		return err
	}

	currentBlock, err := m.client.BlockNumber(ctx)
	if err != nil {
		return err
	}
	// Exclude old finalized records at the repository; young chains use bound 0, and
	// isFinal still checks each result
	var confirmedFrom uint64
	if cutoff := confirmations + postConfirmationReorgWindow; currentBlock > cutoff {
		confirmedFrom = currentBlock - cutoff
	}
	all, err := m.txs.ListUnfinalized(ctx, confirmedFrom)
	if err != nil {
		return err
	}

	for _, tx := range all {
		// txindex.ReconcileStackTransactions owns event-derived records and their statuses;
		// skip them here because they lack a signed payload and meaningful nonce
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

// isFinal accepts TxReplaced and TxFailed immediately, and TxConfirmed once
// confirmations+postConfirmationReorgWindow blocks follow its block
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
		// A mined nonce beyond this attempt, without its receipt, is treated as external
		// nonce consumption
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
		// A previously observed receipt is missing; mark the transaction reorged
		return m.markReorged(ctx, tx)
	default:
		return nil
	}
}

// nonceConsumedExternally checks whether tx.From's mined nonce exceeds tx.Nonce after a
// missing receipt
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

// rebroadcastUnknown resends persisted bytes unchanged and ignores send errors,
// including already-known or nonce-too-low responses. RefreshStatuses resolves the
// outcome from receipts or a mined nonce beyond this attempt.
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
	// Skip canonical comparison when BlockHash is zero, as in hand-built test receipts
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

// isCanonicalBlock compares wantHash with the node's header at number, detecting
// replacement blocks at the same height
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

// markNeedsIntervention records exhausted replacement attempts and returns an error.
// Replace signs and broadcasts nothing; needsInterventionTx then blocks Submit.
func (m *txManager) markNeedsIntervention(ctx context.Context, tx *models.Transaction) error {
	tx.Status = models.TxNeedsIntervention
	tx.UpdatedAt = time.Now().UTC()
	if err := m.txs.Update(ctx, tx); err != nil {
		return fmt.Errorf("blockchain: mark %s needs-intervention: %w", tx.ID, err)
	}
	return fmt.Errorf("blockchain: transaction %s exhausted its replacement-attempt limit (%d); marked for operator intervention", tx.ID, m.maxReplacementAttempts)
}

// reconcileReplacements uses Replaces to restore original Replaced/ReplacedBy links
// after interrupted persistence. It is safe to repeat each tick. Skip TxFailed
// replacements: their send was rejected or abandoned, and Replace attempts to restore
// the original to Pending; linking them would displace a live original.
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

// Replace requires the original From's Signer and reuses its nonce. It takes the local
// lock and optional lease before loading, then builds/signs with bumped fees and
// rechecks caps.
// It persists replacement bytes and conditionally links the original by version before
// broadcast, rejecting stale writers even after lease renewal. RefreshStatuses repairs
// interrupted links and resolves TxBroadcastUnknown by receipt lookup or identical
// rebroadcast.
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

	// Load status, replacement count and version under the lock and optional lease
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
	// Check the attempt limit before signing or broadcasting
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

	// Decode the original to bump from the larger of its fees and fresh suggestions, even
	// after fees fall. Missing or undecodable SignedRawTx leaves only fresh suggestions.
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
		// The replacement is durable; RefreshStatuses repairs the missing link through
		// Replaces and resumes broadcast recovery
		return nil, fmt.Errorf("blockchain: mark original transaction replaced: %w", err)
	} else if !ok {
		// A concurrent writer changed the original. Abandon this replacement without
		// broadcast to avoid competing for its nonce.
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
		// Treat the send as rejected: mark the replacement Failed and attempt to restore the
		// original to Pending with ReplacedBy cleared. Keep ReplacementCount so repeated bad
		// bumps exhaust the limit. The version-conditional revert cannot overwrite a newer
		// writer.
		failedAt := time.Now().UTC()
		newRecord.Status = models.TxFailed
		newRecord.UpdatedAt = failedAt
		_ = m.txs.Update(ctx, newRecord)
		old.Status = models.TxPending
		old.ReplacedBy = ""
		old.ReplacementCount = newRecord.ReplacementCount
		old.UpdatedAt = failedAt
		if ok, uerr := m.txs.UpdateConditional(ctx, old, oldVersion+1); uerr == nil && !ok {
			// Preserve the concurrent writer's state. reconcileReplacements skips the Failed
			// replacement even if this revert fails.
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

// replacementMinBumpPercent matches geth's default --txpool.pricebump 10, applied to
// fee and tip caps. bumpFeeField enforces this floor over the original even when
// callers request a smaller bump.
const replacementMinBumpPercent = 10

// ceilPercent returns ceil(v * (100+pct) / 100).
func ceilPercent(v *big.Int, pct int64) *big.Int {
	out := new(big.Int).Mul(v, big.NewInt(100+pct))
	out.Add(out, big.NewInt(99))
	return out.Div(out, big.NewInt(100))
}

// bumpFeeField rounds up a bumpPercent increase over max(original, current), then
// enforces replacementMinBumpPercent and at least 1 wei above original. These guards
// handle falling suggestions, integer rounding and below-minimum caller bumps. A nil
// original bumps only the current suggestion.
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

// buildBumpedTx reuses the nonce and buildTx's fee-mode selection, bumps each fee from
// max(original, current), then rechecks caps against bumped values. orig may be nil;
// original fees apply only when transaction types match.
func (m *txManager) buildBumpedTx(ctx context.Context, from, to common.Address, data []byte, value *big.Int, nonce uint64, bumpPercent int64, orig *types.Transaction) (*types.Transaction, error) {
	base, err := m.buildTx(ctx, from, to, data, value, nonce)
	if err != nil {
		return nil, err
	}
	// Compare original fees only when its transaction type matches the rebuild
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
		// Keep the EIP-1559 fee cap at least as high as the tip cap, including after BaseFee
		// falls
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
