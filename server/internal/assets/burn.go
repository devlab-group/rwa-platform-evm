package assets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/auditpkg"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/eip712"
)

// BurnService errors; ErrBurnInvalid covers malformed amounts and invalid metadata
var (
	ErrBurnInvalid          = errors.New("assets: invalid burn")
	ErrBurnNotFound         = errors.New("assets: burn not found")
	ErrBurnRecordNotMinted  = errors.New("assets: record is not Minted; only a Minted record can be burned")
	ErrBurnExceedsRemaining = errors.New("assets: burn amount exceeds the record's remaining amount")
	ErrBurnAlreadyBurned    = errors.New("assets: burn is already Burned and cannot be reissued")
	ErrBurnConcurrentUpdate = errors.New("assets: burn was concurrently modified; re-read and retry")
)

// CreateBurnRequest mirrors components.schemas.CreateBurnRequest in api/openapi.yaml
type CreateBurnRequest struct {
	RecordID string
	Amount   string
	Proofs   []ProofRecord
}

// BurnService prepares burns against Minted records and reconciles Burned events.
// It holds no signing key; anyone with a valid auditor signature may submit a burn.
type BurnService struct {
	records *RecordService
	burns   repository.BurnRepository

	// mu serializes reservation changes within this service instance.
	// Multiple instances need a shared database guard.
	mu sync.Mutex
}

// NewBurnService shares rs's records, auditor, domain, Vault and IPFS publisher
func NewBurnService(rs *RecordService, burns repository.BurnRepository) *BurnService {
	return &BurnService{records: rs, burns: burns}
}

// Remaining subtracts Burned and unexpired Pending amounts from record.Amount,
// excluding skip (empty to include all burns)
func (s *BurnService) Remaining(ctx context.Context, record *models.AssetRecord, skip string) (*big.Int, error) {
	total, ok := new(big.Int).SetString(record.Amount, 10)
	if !ok {
		return nil, fmt.Errorf("assets: invalid stored amount %q", record.Amount)
	}
	burns, err := s.burns.ListByRecord(ctx, record.RecordID)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	for _, b := range burns {
		if b.BurnID == skip {
			continue
		}
		if b.Status != models.BurnStatusBurned && b.ValidUntil <= now {
			continue // expired reservation
		}
		n, ok := new(big.Int).SetString(b.Amount, 10)
		if !ok {
			return nil, fmt.Errorf("assets: invalid stored burn amount %q", b.Amount)
		}
		total.Sub(total, n)
	}
	return total, nil
}

// buildBurnMetadata replaces issuance.amount, createdAt and proofs in record metadata
func buildBurnMetadata(record *models.AssetRecord, amount string, proofs []ProofRecord, now time.Time) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(record.MetadataRaw, &top); err != nil {
		return nil, fmt.Errorf("assets: decode record metadata: %w", err)
	}
	issuance, err := json.Marshal(struct {
		Amount string `json:"amount"`
		Unit   string `json:"unit"`
	}{amount, record.Unit})
	if err != nil {
		return nil, err
	}
	top["issuance"] = issuance
	top["createdAt"], _ = json.Marshal(now.Format(time.RFC3339))
	delete(top, "proofs")
	if len(proofs) > 0 {
		if top["proofs"], err = json.Marshal(proofs); err != nil {
			return nil, err
		}
	}
	return json.Marshal(top)
}

// CreateBurn stores a validated Pending burn with a UUID, derived operationId,
// random nonce and the mint expiry window, after optional metadata publication
func (s *BurnService) CreateBurn(ctx context.Context, profile *Profile, projectID string, req CreateBurnRequest) (*models.Burn, error) {
	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || !decimalAmountPattern.MatchString(req.Amount) || amount.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount must be a positive base-unit decimal string", ErrBurnInvalid)
	}
	amountStr := amount.String() // Canonical decimal amount without leading zeros

	s.mu.Lock()
	defer s.mu.Unlock()

	record, err := s.records.records.Get(ctx, req.RecordID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrRecordNotFound
		}
		return nil, err
	}
	if record.Status != models.RecordStatusMinted {
		return nil, ErrBurnRecordNotMinted
	}
	remaining, err := s.Remaining(ctx, record, "")
	if err != nil {
		return nil, err
	}
	if amount.Cmp(remaining) > 0 {
		return nil, fmt.Errorf("%w: %s requested, %s remaining", ErrBurnExceedsRemaining, amountStr, remaining)
	}

	now := time.Now().UTC()
	raw, err := buildBurnMetadata(record, amountStr, req.Proofs, now)
	if err != nil {
		return nil, err
	}
	result, meta := ValidateRecord(profile, raw, amountStr, projectID)
	if !result.Valid {
		return nil, fmt.Errorf("%w: %v", ErrBurnInvalid, result.Errors)
	}
	nonce, err := randomUint256()
	if err != nil {
		return nil, err
	}
	burnID := uuid.NewString()
	burn := &models.Burn{
		BurnID: burnID, RecordID: record.RecordID, ProjectID: projectID, Status: models.BurnStatusPending,
		OperationID: crypto.Keccak256Hash([]byte(burnID)).Hex(), Amount: amountStr, Unit: record.Unit,
		MetadataRaw: meta.Canonical, MetadataDigest: meta.DigestHex(), CID: meta.CID,
		Nonce: nonce.String(), ValidUntil: now.Add(attestationValidUntilWindow).Unix(),
		CreatedAt: now, UpdatedAt: now,
	}
	// Publish before persistence; a failed Create may leave an orphaned publication
	if err := s.records.publish(ctx, burnID, meta.Canonical); err != nil {
		return nil, err
	}
	if err := s.burns.Create(ctx, burn); err != nil {
		return nil, err
	}
	return burn, nil
}

// ReissueBurn refreshes a Pending burn's nonce and expiry, keeping its ID, metadata
// and amount; it rejects Burned burns or amounts above Remaining excluding this burn
func (s *BurnService) ReissueBurn(ctx context.Context, burnID string) (*models.Burn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	burn, err := s.burns.Get(ctx, burnID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrBurnNotFound
		}
		return nil, err
	}
	if burn.Status == models.BurnStatusBurned {
		return nil, ErrBurnAlreadyBurned
	}
	record, err := s.records.records.Get(ctx, burn.RecordID)
	if err != nil {
		return nil, err
	}
	remaining, err := s.Remaining(ctx, record, burn.BurnID)
	if err != nil {
		return nil, err
	}
	amount, ok := new(big.Int).SetString(burn.Amount, 10)
	if !ok {
		return nil, fmt.Errorf("assets: invalid stored burn amount %q", burn.Amount)
	}
	if amount.Cmp(remaining) > 0 {
		return nil, fmt.Errorf("%w: %s requested, %s remaining", ErrBurnExceedsRemaining, burn.Amount, remaining)
	}

	nonce, err := randomUint256()
	if err != nil {
		return nil, err
	}
	expectedVersion := burn.Version
	burn.Nonce = nonce.String()
	burn.ValidUntil = time.Now().UTC().Add(attestationValidUntilWindow).Unix()
	burn.UpdatedAt = time.Now().UTC()
	ok, err = s.burns.UpdateConditional(ctx, burn, expectedVersion)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrBurnConcurrentUpdate
	}
	burn.Version = expectedVersion + 1
	return burn, nil
}

// BuildPackage rebuilds a BurnAttestation .rwa archive from the stored burn and current auditor
func (s *BurnService) BuildPackage(burn *models.Burn, profileDigest [32]byte, profileRaw []byte) ([]byte, error) {
	operationID, err := hexToBytes32Local(burn.OperationID)
	if err != nil {
		return nil, err
	}
	metadataDigest, err := hexToBytes32Local(burn.MetadataDigest)
	if err != nil {
		return nil, err
	}
	amount, ok := new(big.Int).SetString(burn.Amount, 10)
	if !ok {
		return nil, fmt.Errorf("assets: invalid stored amount %q", burn.Amount)
	}
	nonce, ok := new(big.Int).SetString(burn.Nonce, 10)
	if !ok {
		return nil, fmt.Errorf("assets: invalid stored nonce %q", burn.Nonce)
	}
	attestation := eip712.BurnAttestation{
		Auditor: s.records.currentAuditor(), ProfileDigest: profileDigest, OperationID: operationID,
		MetadataDigest: metadataDigest, Amount: amount, Nonce: nonce, ValidUntil: uint64(burn.ValidUntil),
		Vault: s.records.vaultAddr,
	}
	typedData := auditpkg.NewBurnTypedDataDoc(s.records.eip712Domain, attestation)
	zipBytes, _, err := auditpkg.BuildPackage("BurnAttestation", profileDigest, metadataDigest, profileRaw, burn.MetadataRaw, typedData, nil)
	return zipBytes, err
}

// ReconcileBurned matches indexed Burned events by chain, controller and operationId,
// marking known burns Burned with the transaction hash, even after expiry or reissue.
// Unknown operations and already Burned records are ignored.
func (s *BurnService) ReconcileBurned(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, controllerAddr string) error {
	events, err := chainEvents.ListByName(ctx, chainID, controllerAddr, "Burned")
	if err != nil {
		return fmt.Errorf("assets: list Burned events: %w", err)
	}
	for _, e := range events {
		opID, _ := e.Data["operationId"].(string)
		if opID == "" {
			continue
		}
		burn, err := s.burns.GetByOperationID(ctx, strings.ToLower(opID))
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("assets: look up burn for operation %s: %w", opID, err)
		}
		if burn.Status == models.BurnStatusBurned {
			continue
		}
		expectedVersion := burn.Version
		burn.Status = models.BurnStatusBurned
		burn.BurnTxHash = e.TxHash
		burn.UpdatedAt = time.Now().UTC()
		// Retry a concurrent reissue's version conflict on the next tick
		if _, err := s.burns.UpdateConditional(ctx, burn, expectedVersion); err != nil {
			return fmt.Errorf("assets: update burn %s to Burned: %w", burn.BurnID, err)
		}
	}
	return nil
}
