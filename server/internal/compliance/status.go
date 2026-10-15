package compliance

import (
	"context"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
)

// StatusService relays ComplianceRegistry.setStatus with the compliance hot key.
// Multisig actions reach the same contract outside this service.
type StatusService struct {
	txs      blockchain.TxManager
	registry bindings.ComplianceRegistry
	contract common.Address
	signer   blockchain.Signer
}

// NewStatusService accepts a blockchain.Signer, including StaticKeySigner and
// internal/keys backends
func NewStatusService(txs blockchain.TxManager, contract common.Address, signer blockchain.Signer) *StatusService {
	return &StatusService{txs: txs, registry: bindings.NewComplianceRegistry(), contract: contract, signer: signer}
}

// SetStatus submits one setStatus(account, status, validUntil) transaction
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
		// Repeating the same (account, status, validUntil) writes the same state and moves no
		// value, enabling same-key retries after TxReorged or TxNonceConsumedExternally
		AllowReorgRetry: true,
	})
}

// StatusFromString maps the API's WalletStatus and SetStatusRequest string
// values to the on-chain encoding
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
