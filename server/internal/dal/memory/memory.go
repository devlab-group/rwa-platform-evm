// Package memory implements repository interfaces for unit tests and
// PERSISTENCE_MODE=memory local runs. Data is volatile; production configuration
// requires MongoDB.
package memory

import (
	"github.com/rwa-platform/server/internal/dal/repository"
)

// New returns a Repositories value backed entirely by empty in-memory repositories
func New() *repository.Repositories {
	chainEvents := NewChainEventRepository()
	blockHashes := NewBlockHashRepository()
	checkpoints := NewIndexerCheckpointRepository()
	return &repository.Repositories{
		Projects:             NewProjectRepository(),
		AssetProfiles:        NewAssetProfileRepository(),
		AssetRecords:         NewAssetRecordRepository(),
		Burns:                NewBurnRepository(),
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
