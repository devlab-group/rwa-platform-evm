package assets

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/auditpkg"
	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/eip712"
)

// ipfsPinner is the part of ipfs.Client that RecordService uses, declared here
// so tests can stub it
type ipfsPinner interface {
	AddRaw(ctx context.Context, data []byte) (string, error)
}

// attestationValidUntilWindow is how long after creation or reissue a record's
// mint attestation stays valid, 30 days
const attestationValidUntilWindow = 30 * 24 * time.Hour

// CreateRecordRequest mirrors Schemas.CreateRecordRequest in api/openapi.yaml
type CreateRecordRequest struct {
	RecordID string
	Asset    json.RawMessage
	Amount   string
	Proofs   []ProofRecord
}

// RecordService creates records and .rwa packages, and projects Minted and
// AuditorChanged events onto record status and auditor address. It holds no signing
// key; the admin wallet broadcasts auditor-signed mints.
type RecordService struct {
	records        repository.AssetRecordRepository
	packages       repository.AuditPackageRepository
	attestations   repository.AttestationRepository
	ipfs           ipfsPinner
	controller     bindings.SupplyController
	controllerAddr common.Address
	vaultAddr      common.Address
	eip712Domain   eip712.Domain

	// auditorMu guards auditor, which SetAuditor, ReconcileAuditor and
	// ReconcileAuditorLive change after construction while BuildPackage reads it
	auditorMu sync.RWMutex
	auditor   common.Address
	// baselineAuditor is the auditor passed to NewRecordService and is never
	// changed. ReconcileAuditor restores it when no AuditorChanged event remains.
	baselineAuditor common.Address
}

// NewRecordService returns a RecordService whose auditor and baseline auditor
// are both auditor
func NewRecordService(
	records repository.AssetRecordRepository, packages repository.AuditPackageRepository, attestations repository.AttestationRepository,
	ipfsClient ipfsPinner, controllerAddr, vaultAddr common.Address,
	eip712Domain eip712.Domain, auditor common.Address,
) *RecordService {
	return &RecordService{
		records: records, packages: packages, attestations: attestations, ipfs: ipfsClient,
		controller: bindings.NewSupplyController(), controllerAddr: controllerAddr, vaultAddr: vaultAddr,
		eip712Domain: eip712Domain, auditor: auditor, baselineAuditor: auditor,
	}
}

// SetAuditor sets the auditor address that BuildPackage uses from then on. It is
// safe to call concurrently with BuildPackage.
func (s *RecordService) SetAuditor(addr common.Address) {
	s.auditorMu.Lock()
	defer s.auditorMu.Unlock()
	s.auditor = addr
}

// currentAuditor returns the live auditor address
func (s *RecordService) currentAuditor() common.Address {
	s.auditorMu.RLock()
	defer s.auditorMu.RUnlock()
	return s.auditor
}

// CreateRecord validates req's metadata against profile, then stores a Pending record
// with a random nonce, recordKey and 30-day validUntil before optional IPFS
// publication. A duplicate recordId returns repository.ErrAlreadyExists.
func (s *RecordService) CreateRecord(ctx context.Context, profile *Profile, projectID string, req CreateRecordRequest) (*models.AssetRecord, error) {
	proofs := req.Proofs
	if proofs == nil {
		proofs = []ProofRecord{}
	}
	envelope := struct {
		PlatformVersion string          `json:"platformVersion"`
		ProjectID       string          `json:"projectId"`
		RecordID        string          `json:"recordId"`
		Asset           json.RawMessage `json:"asset"`
		Issuance        struct {
			Amount string `json:"amount"`
			Unit   string `json:"unit"`
		} `json:"issuance"`
		Proofs    []ProofRecord `json:"proofs,omitempty"`
		CreatedAt string        `json:"createdAt"`
	}{
		PlatformVersion: "1.0", ProjectID: projectID, RecordID: req.RecordID, Asset: req.Asset,
		Proofs: proofs, CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	envelope.Issuance.Amount = req.Amount
	envelope.Issuance.Unit = profile.TokenUnit

	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("assets: marshal metadata envelope: %w", err)
	}

	result, meta := ValidateRecord(profile, raw, req.Amount, projectID)
	if !result.Valid {
		return nil, fmt.Errorf("assets: invalid record: %v", result.Errors)
	}

	nonce, err := randomUint256()
	if err != nil {
		return nil, err
	}
	recordKey := crypto.Keccak256Hash([]byte(req.RecordID))
	now := time.Now().UTC()

	record := &models.AssetRecord{
		RecordID: req.RecordID, ProjectID: projectID, Status: models.RecordStatusPending,
		AssetRaw: req.Asset, MetadataRaw: meta.Canonical, Amount: req.Amount, Unit: profile.TokenUnit,
		Proofs: toDomainProofs(proofs), MetadataDigest: meta.DigestHex(), CID: meta.CID,
		RecordKey: recordKey.Hex(), Nonce: nonce.String(), ValidUntil: now.Add(attestationValidUntilWindow).Unix(),
		CreatedAt: now, UpdatedAt: now,
	}
	// Reserve before Publish to reject duplicate RecordIDs. The replication layer also
	// rejects changed CIDs; see ipfs.ErrPublicationCIDConflict.
	if err := s.records.Create(ctx, record); err != nil {
		return nil, err
	}

	if s.ipfs != nil {
		// use Publish, which keys the publication record by RecordID, when the
		// client has it (ipfs.ReplicationManager does; a plain Client does not)
		if p, ok := s.ipfs.(interface {
			Publish(ctx context.Context, id string, data []byte) (string, error)
		}); ok {
			if _, err := p.Publish(ctx, req.RecordID, meta.Canonical); err != nil {
				return nil, fmt.Errorf("assets: pin metadata: %w", err)
			}
		} else if _, err := s.ipfs.AddRaw(ctx, meta.Canonical); err != nil {
			return nil, fmt.Errorf("assets: pin metadata: %w", err)
		}
	}
	return record, nil
}

// ErrRecordNotFound is returned by ReissueRecord when no record has the given id
var ErrRecordNotFound = errors.New("assets: record not found")

// ErrRecordNotReissuable rejects non-Pending reissues. Changing Signed or Minted nonces
// could strand or double-count a mint; Draft and Rejected records cannot mint.
var ErrRecordNotReissuable = errors.New("assets: record is not Pending; only a Pending record may be reissued")

// ReissueRecord refreshes a Pending record's nonce and validUntil, allowing recovery
// after expiry. recordId, metadata, digests, recordKey, amount and unit stay unchanged.
// Earlier packages and signatures contain the old nonce, so rebuild and re-sign to
// match the updated record.
func (s *RecordService) ReissueRecord(ctx context.Context, recordID string) (*models.AssetRecord, error) {
	record, err := s.records.Get(ctx, recordID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrRecordNotFound
		}
		return nil, err
	}
	if record.Status != models.RecordStatusPending {
		return nil, ErrRecordNotReissuable
	}

	nonce, err := randomUint256()
	if err != nil {
		return nil, err
	}
	expectedVersion := record.Version
	record.Status = models.RecordStatusPending
	record.Nonce = nonce.String()
	record.ValidUntil = time.Now().UTC().Add(attestationValidUntilWindow).Unix()
	record.UpdatedAt = time.Now().UTC()
	// conditional on the version read above, so of two concurrent reissues only
	// one succeeds
	ok, err := s.records.UpdateConditional(ctx, record, expectedVersion)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrRecordConcurrentlyModified
	}
	record.Version = expectedVersion + 1
	return record, nil
}

// ErrRecordConcurrentlyModified signals a lost version-conditional update. Re-read the
// record before deciding how to proceed.
var ErrRecordConcurrentlyModified = errors.New("assets: record was concurrently modified; re-read and retry")

func toDomainProofs(proofs []ProofRecord) []models.Proof {
	out := make([]models.Proof, len(proofs))
	for i, p := range proofs {
		out[i] = models.Proof{Type: p.Type, SHA256: p.SHA256, URI: p.URI}
	}
	return out
}

func randomUint256() (*big.Int, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("assets: generate nonce: %w", err)
	}
	return new(big.Int).SetBytes(b), nil
}

// packageFields holds the values a package and its MintAttestation are built
// from
type packageFields struct {
	auditor        common.Address
	profileDigest  [32]byte
	metadataDigest [32]byte
	recordKey      [32]byte
	amount         *big.Int
	nonce          *big.Int
	validUntil     uint64
	vault          common.Address
}

// buildFromFields builds the package for f and returns the ZIP bytes, their
// SHA-256, and the hex EIP-712 digest of the MintAttestation
func (s *RecordService) buildFromFields(recordID string, f packageFields, profileRaw, metadataRaw []byte) (zipBytes []byte, packageSHA256, typedDataDigestHex string, err error) {
	attestation := eip712.MintAttestation{
		Auditor: f.auditor, ProfileDigest: f.profileDigest, RecordKey: f.recordKey, MetadataDigest: f.metadataDigest,
		Amount: f.amount, Nonce: f.nonce, ValidUntil: f.validUntil, Vault: f.vault,
	}
	typedData := auditpkg.NewMintTypedDataDoc(s.eip712Domain, recordID, attestation)
	digest := eip712.MintDigest(s.eip712Domain, attestation)

	zipBytes, packageSHA256, err = auditpkg.BuildPackage("MintAttestation", f.profileDigest, f.metadataDigest, profileRaw, metadataRaw, typedData, nil)
	if err != nil {
		return nil, "", "", err
	}
	return zipBytes, packageSHA256, "0x" + hex.EncodeToString(digest[:]), nil
}

// liveFieldsFor takes packageFields from record's stored fields, including its
// current nonce and validUntil, and from the service's current auditor and vault
func (s *RecordService) liveFieldsFor(record *models.AssetRecord, profileDigest [32]byte) (packageFields, error) {
	recordKey, err := hexToBytes32Local(record.RecordKey)
	if err != nil {
		return packageFields{}, err
	}
	metadataDigestBytes, err := hexToBytes32Local(record.MetadataDigest)
	if err != nil {
		return packageFields{}, err
	}
	amount, ok := new(big.Int).SetString(record.Amount, 10)
	if !ok {
		return packageFields{}, fmt.Errorf("assets: invalid stored amount %q", record.Amount)
	}
	nonce, ok := new(big.Int).SetString(record.Nonce, 10)
	if !ok {
		return packageFields{}, fmt.Errorf("assets: invalid stored nonce %q", record.Nonce)
	}
	return packageFields{
		auditor: s.currentAuditor(), profileDigest: profileDigest, metadataDigest: metadataDigestBytes,
		recordKey: recordKey, amount: amount, nonce: nonce, validUntil: uint64(record.ValidUntil), vault: s.vaultAddr,
	}, nil
}

// BuildPackage combines profileRaw and profileDigest with the record's canonical
// metadata, recordKey, nonce, validUntil, amount and metadataDigest, plus the service's
// chain ID, controller, current auditor and vault. Identical inputs produce identical
// .rwa bytes; auditor rotation changes the package. It stores a models.AuditPackage
// build row.
func (s *RecordService) BuildPackage(ctx context.Context, record *models.AssetRecord, profileDigest [32]byte, profileRaw []byte) ([]byte, error) {
	fields, err := s.liveFieldsFor(record, profileDigest)
	if err != nil {
		return nil, err
	}
	zipBytes, packageSHA256, typedDataDigestHex, err := s.buildFromFields(record.RecordID, fields, profileRaw, record.MetadataRaw)
	if err != nil {
		return nil, err
	}
	if err := s.packages.Upsert(ctx, packageRecordFromFields(record.RecordID, fields, packageSHA256, typedDataDigestHex, len(zipBytes), record.Version, record.CID)); err != nil {
		return nil, err
	}
	return zipBytes, nil
}

// packageRecordFromFields returns the models.AuditPackage row for a build,
// tagged with the record version and CID it was built from
func packageRecordFromFields(recordID string, f packageFields, packageSHA256, typedDataDigestHex string, size int, recordVersion int, cid string) *models.AuditPackage {
	return &models.AuditPackage{
		RecordID: recordID, PackageSHA256: packageSHA256, Size: int64(size),
		RecordVersion:   recordVersion,
		CID:             cid,
		Auditor:         f.auditor.Hex(),
		ProfileDigest:   "0x" + hex.EncodeToString(f.profileDigest[:]),
		MetadataDigest:  "0x" + hex.EncodeToString(f.metadataDigest[:]),
		RecordKey:       "0x" + hex.EncodeToString(f.recordKey[:]),
		Amount:          f.amount.String(),
		Nonce:           f.nonce.String(),
		ValidUntil:      int64(f.validUntil),
		Vault:           f.vault.Hex(),
		TypedDataDigest: typedDataDigestHex,
		CreatedAt:       time.Now().UTC(),
	}
}

func hexToBytes32Local(s string) ([32]byte, error) {
	var out [32]byte
	trimmed := s
	if len(trimmed) >= 2 && trimmed[0:2] == "0x" {
		trimmed = trimmed[2:]
	}
	if len(trimmed) != 64 {
		return out, fmt.Errorf("assets: expected 32-byte hex, got %d chars", len(trimmed))
	}
	b, err := hex.DecodeString(trimmed)
	if err != nil {
		return out, err
	}
	copy(out[:], b)
	return out, nil
}
