// Package memory implements every internal/dal/repository interface in memory. It
// backs unit tests, so business logic can be exercised without a database, and the
// PERSISTENCE_MODE=memory setting for local runs. Its data is not durable and the
// platform refuses to start with it in production. MongoDB is the production
// store.
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
