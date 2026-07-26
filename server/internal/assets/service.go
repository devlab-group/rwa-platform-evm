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

// ipfsPinner is the subset of internal/ipfs.Client this package needs,
// declared locally to avoid an import-cycle risk and to keep the workflow
// testable with a trivial stub.
type ipfsPinner interface {
	AddRaw(ctx context.Context, data []byte) (string, error)
}

// attestationValidUntil bounds how long a mint attestation remains signable
// after package creation — no more than 30 days.
const attestationValidUntilWindow = 30 * 24 * time.Hour

// CreateRecordRequest mirrors api Schemas.CreateRecordRequest.
type CreateRecordRequest struct {
	RecordID string
	Asset    json.RawMessage
	Amount   string
	Proofs   []ProofRecord
}

// RecordService creates and packages asset records.
// The auditor-signed mint is no longer relayed by the server — it is broadcast
// from the admin's wallet — so this service holds no relayer hot key; it only
// builds the .rwa evidence package and projects the on-chain Minted event onto
// record status (ReconcileMinted).
type RecordService struct {
	records        repository.AssetRecordRepository
	packages       repository.AuditPackageRepository
	attestations   repository.AttestationRepository
	ipfs           ipfsPinner
	controller     bindings.SupplyController
	controllerAddr common.Address
	vaultAddr      common.Address
	eip712Domain   eip712.Domain

	// auditorMu guards auditor: it is set at construction from whatever was
	// known then, but that value can be empty (factory activation didn't
	// source it from the deployed project) or stale (an on-chain auditor
	// rotation via SupplyController.setAuditor, performed outside this server,
	// otherwise never reaches a running process). SetAuditor/ReconcileAuditor/
	// ReconcileAuditorLive all update it after construction, concurrently with
	// BuildPackage/RelaySignedResult reading it — hence the lock rather than a
	// plain field.
	auditorMu sync.RWMutex
	auditor   common.Address
	// baselineAuditor is the constructor's auditor value, held separately from
	// auditor and NEVER mutated after construction, so mutable authorities can
	// be reconciled from live contract calls or from this persisted deployment
	// baseline plus surviving events. ReconcileAuditor falls back to this when
	// no surviving AuditorChanged event exists (the rotation that set the
	// current auditor was itself rolled back by a reorg), rather than leaving
	// auditor stuck at an orphaned value with nothing left to restore it — see
	// ReconcileAuditor's doc comment.
	baselineAuditor common.Address
}

// NewRecordService constructs a RecordService.
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

// SetAuditor updates the auditor address BuildPackage/RelaySignedResult use
// for every subsequent call. Safe to call concurrently with those.
func (s *RecordService) SetAuditor(addr common.Address) {
	s.auditorMu.Lock()
	defer s.auditorMu.Unlock()
	s.auditor = addr
}

// currentAuditor returns the auditor address in effect right now.
func (s *RecordService) currentAuditor() common.Address {
	s.auditorMu.RLock()
	defer s.auditorMu.RUnlock()
	return s.auditor
}

// CreateRecord validates req against profile, canonicalizes+pins the
// metadata envelope, and persists a Pending asset record with a freshly
// generated nonce/recordKey/validUntil ready for package building.
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
	// Reserve the immutable, create-once asset record BEFORE any external
	// publication. A duplicate create for an existing RecordID fails here
	// (ErrAlreadyExists) and never reaches Publish, so it can no longer
	// overwrite the durable-publication row's CID out from under the original
	// signed record. The record binds RecordID -> CID immutably, so
	// every subsequent publish for this id carries the same CID (which the
	// replication layer additionally enforces — see ipfs.ErrPublicationCIDConflict).
	if err := s.records.Create(ctx, record); err != nil {
		return nil, err
	}

	if s.ipfs != nil {
		// Prefer Publish (keyed by this record's own RecordID, a
		// meaningful lookup key) over the bare AddRaw(ctx,data) shape when
		// the configured client supports it (*ipfs.ReplicationManager
		// does; a plain Client/FakeClient does not). The mint gate looks up
		// durable-publication status by RecordID, so this is what makes that
		// lookup key exist in the first place.
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

// ErrRecordNotFound is returned by ReissueRecord when no record exists for
// the given id, so the handler can map it to 404 rather than a generic 500.
var ErrRecordNotFound = errors.New("assets: record not found")

// ErrRecordNotReissuable is returned by ReissueRecord for a record that is
// not in Pending state. Reissue is audited recovery for a record whose
// attestation window lapsed BEFORE it was minted; once a record is Signed or
// Minted, rolling its nonce could strand or double-count a real on-chain mint,
// and Rejected/Draft records were never eligible to mint in the first place
// (see api/openapi.yaml reissueRecord).
var ErrRecordNotReissuable = errors.New("assets: record is not Pending; only a Pending record may be reissued")

// ReissueRecord assigns a fresh nonce and validUntil to an existing Pending
// record WITHOUT changing its immutable identity (recordId, metadata,
// digests, recordKey, amount, unit all unchanged), so a record whose
// attestation window lapsed before it was signed/relayed can be re-attested
// without violating record-ID uniqueness. Any previously built
// .rwa package or auditor signature was bound to the OLD nonce and no longer
// matches; the operator rebuilds the package and the auditor re-signs after
// this. Admin-only and idempotency-keyed at the HTTP layer.
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
	// Conditional on the version read above, so two concurrent reissues can't
	// both roll the nonce — the loser's conditional update simply fails
	// instead of clobbering.
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

// ErrRecordConcurrentlyModified is returned when a version-conditional record
// transition (reissue or relay) lost a race to a concurrent one. The caller
// should re-read the record and decide again rather than blindly retry.
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

// packageFields is everything auditpkg.BuildPackage's inputs are derived
// from — shared by the live (Pending) and frozen (Signed/Minted)
// reconstruction paths in BuildPackage below, so both build the exact same
// way from whichever source (live record state, or a frozen
// models.AuditPackage) supplied them.
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

// buildFromFields runs auditpkg.BuildPackage for f, returning the ZIP
// bytes, its SHA-256, and the hex-encoded EIP-712 typed-data digest it
// embeds (retained so the frozen path can persist it without a second
// computation elsewhere).
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

// liveFieldsFor derives packageFields from record's CURRENT (possibly
// reissued) nonce/validUntil and the server's CURRENT auditor — only valid
// while record is still Pending; see BuildPackage's doc comment.
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

// BuildPackage assembles the .rwa audit package for record, using
// profileRaw (the project's canonicalized Asset Profile bytes) and the
// record's stored canonical metadata bytes. It rebuilds deterministically
// from the record's own persisted fields (recordKey, nonce, validUntil,
// amount, metadataDigest) plus the server's current auditor and vault, so a
// re-download always yields the same bytes for the same record state (see
// auditpkg.BuildPackage's determinism, TestBuildPackageIsDeterministic).
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

// packageRecordFromFields constructs the models.AuditPackage row BuildPackage
// persists as evidence, bound to the record version and CID it was built from.
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
