package compliance

import (
	"context"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
)

// StatusService submits ComplianceRegistry.setStatus transactions using the
// server's compliance hot key. Manual compliance
// actions performed by a multisig instead go through the same contract but
// outside this server-signed path; this service only covers the automated/
// hot-key operator flow.
type StatusService struct {
	txs      blockchain.TxManager
	registry bindings.ComplianceRegistry
	contract common.Address
	signer   blockchain.Signer
}

// NewStatusService constructs a StatusService. signer is typically a
// StaticKeySigner (tests, simple deployments) or an internal/keys-backed
// Signer (local-keystore/vault/kms-mock).
func NewStatusService(txs blockchain.TxManager, contract common.Address, signer blockchain.Signer) *StatusService {
	return &StatusService{txs: txs, registry: bindings.NewComplianceRegistry(), contract: contract, signer: signer}
}

// SetStatus submits one setStatus(account, status, validUntil) transaction.
func (s *StatusService) SetStatus(ctx context.Context, idempotencyKey string, account common.Address, status bindings.ComplianceStatus, validUntil uint64) (*models.Transaction, error) {
	data, err := s.registry.PackSetStatus(account, status, validUntil)
	if err != nil {
		return nil, err
	}
	return s.txs.Submit(ctx, blockchain.SubmitRequest{
		IdempotencyKey: idempotencyKey,
		Kind:           "compliance.setStatus",
		Signer:         s.signer,
		To:             s.contract,
		Data:           data,
		// setStatus is a plain idempotent state write — no
		// value moves, and reapplying the same (account,status,validUntil)
		// twice has no duplicate side effect — so it's safe to opt into
		// same-key resubmission after TxReorged/TxNonceConsumedExternally.
		// This is exactly the recovery path WebhookReconciler.
		// checkSubmitted relies on: without this, its
		// TxReorged handling would now be blocked by the signer-level
		// NeedsIntervention-equivalent guard Submit added for every OTHER
		// Kind that does NOT make this same safety claim.
		AllowReorgRetry: true,
	})
}

// StatusFromString maps the API's WalletStatus/SetStatusRequest string enum
// to the on-chain uint8 encoding.
func StatusFromString(s string) (bindings.ComplianceStatus, bool) {
	switch s {
	case "Unknown":
		return bindings.ComplianceStatusUnknown, true
	case "Allowed":
		return bindings.ComplianceStatusAllowed, true
	case "Blocked":
		return bindings.ComplianceStatusBlocked, true
	default:
		return 0, false
	}
}
