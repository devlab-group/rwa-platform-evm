package assets

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/auditpkg"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/eip712"
)

type stubIPFS struct{ calls int }

func (s *stubIPFS) AddRaw(ctx context.Context, data []byte) (string, error) {
	s.calls++
	return "bafkstub", nil
}

// stubDurabilityIPFS additionally implements Publish (keyed by record id), the
// path CreateRecord prefers.
type stubDurabilityIPFS struct {
	stubIPFS
	published map[string]bool
}

func (s *stubDurabilityIPFS) Publish(ctx context.Context, id string, data []byte) (string, error) {
	s.calls++
	return "bafkstub", nil
}

func newTestService(t *testing.T, auditorKey *ecdsa.PrivateKey, controllerAddr, vaultAddr common.Address) (*RecordService, *memory.AssetRecordRepository) {
	t.Helper()
	records := memory.NewAssetRecordRepository()
	packages := memory.NewAuditPackageRepository()
	attestations := memory.NewAttestationRepository()
	domainCfg := eip712.Domain{
		Name: "RWA-Supply-Attestation", Version: "1", ChainID: big.NewInt(31337), VerifyingContract: controllerAddr,
	}
	auditor := crypto.PubkeyToAddress(auditorKey.PublicKey)
	svc := NewRecordService(records, packages, attestations, &stubIPFS{}, controllerAddr, vaultAddr, domainCfg, auditor)
	return svc, records
}

func TestCreateRecordPersistsPendingRecord(t *testing.T) {
	_, profile := mustProfileAndDigest(t)
	auditorKey, _ := crypto.GenerateKey()
	svc, records := newTestService(t, auditorKey, common.Address{}, common.Address{})

	req := CreateRecordRequest{
		RecordID: "GOLD-BAR-99", Asset: json.RawMessage(`{"serialNumber":"99","weightGrams":"500","purity":"999.9"}`),
		Amount: "500000000000000000000",
	}
	rec, err := svc.CreateRecord(context.Background(), profile, profile.ProjectID, req)
	if err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	if rec.Status != "Pending" {
		t.Errorf("Status = %s, want Pending", rec.Status)
	}
	if rec.RecordKey == "" || rec.MetadataDigest == "" || rec.Nonce == "" {
		t.Errorf("expected recordKey/metadataDigest/nonce to be populated: %+v", rec)
	}

	stored, err := records.Get(context.Background(), "GOLD-BAR-99")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.RecordID != rec.RecordID {
		t.Errorf("stored mismatch")
	}
}

// TestBuildPackageDeterministicRebuild confirms BuildPackage rebuilds the same
// bytes for the same record state (the mint is signed/broadcast from the
// wallet; this is a best-effort evidence rebuild).
func TestBuildPackageDeterministicRebuild(t *testing.T) {
	_, profile := mustProfileAndDigest(t)
	auditorKey, _ := crypto.GenerateKey()
	controllerAddr := common.HexToAddress("0x0000000000000000000000000000000000C0DE")
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000FADE")
	svc, _ := newTestService(t, auditorKey, controllerAddr, vaultAddr)

	req := CreateRecordRequest{
		RecordID: "GOLD-BAR-PKG", Asset: json.RawMessage(`{"serialNumber":"1","weightGrams":"100","purity":"999.9"}`),
		Amount: "100000000000000000000",
	}
	rec, err := svc.CreateRecord(context.Background(), profile, profile.ProjectID, req)
	if err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}

	first, err := svc.BuildPackage(context.Background(), rec, profile.Digest, profile.Canonical)
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	opened, err := auditpkg.OpenPackage(first)
	if err != nil {
		t.Fatalf("OpenPackage: %v", err)
	}
	if opened.Manifest.PrimaryType != "MintAttestation" {
		t.Errorf("PrimaryType = %q", opened.Manifest.PrimaryType)
	}
	second, err := svc.BuildPackage(context.Background(), rec, profile.Digest, profile.Canonical)
	if err != nil {
		t.Fatalf("BuildPackage (rebuild): %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("expected byte-identical package on rebuild for the same record state")
	}
}

func TestReissueRecordRotatesNonceAndExpiryPreservingIdentity(t *testing.T) {
	_, profile := mustProfileAndDigest(t)
	auditorKey, _ := crypto.GenerateKey()
	svc, records := newTestService(t, auditorKey, common.Address{}, common.Address{})

	req := CreateRecordRequest{
		RecordID: "GOLD-BAR-REISSUE", Asset: json.RawMessage(`{"serialNumber":"7","weightGrams":"100","purity":"999.9"}`),
		Amount: "100000000000000000000",
	}
	rec, err := svc.CreateRecord(context.Background(), profile, profile.ProjectID, req)
	if err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	// Simulate a record whose attestation window lapsed before it was minted.
	rec.ValidUntil = time.Now().Add(-time.Hour).Unix()
	if err := records.Update(context.Background(), rec); err != nil {
		t.Fatalf("Update: %v", err)
	}
	oldNonce, oldValidUntil := rec.Nonce, rec.ValidUntil

	reissued, err := svc.ReissueRecord(context.Background(), rec.RecordID)
	if err != nil {
		t.Fatalf("ReissueRecord: %v", err)
	}
	if reissued.Nonce == oldNonce {
		t.Error("expected a fresh nonce, got the same one")
	}
	if reissued.ValidUntil <= oldValidUntil || reissued.ValidUntil <= time.Now().Unix() {
		t.Errorf("expected a fresh future validUntil, got %d", reissued.ValidUntil)
	}
	if reissued.RecordID != rec.RecordID || reissued.MetadataDigest != rec.MetadataDigest ||
		reissued.RecordKey != rec.RecordKey || reissued.CID != rec.CID || reissued.Amount != rec.Amount {
		t.Errorf("reissue changed immutable identity: %+v vs %+v", reissued, rec)
	}
	if reissued.Status != models.RecordStatusPending {
		t.Errorf("Status = %s, want Pending", reissued.Status)
	}
	stored, err := records.Get(context.Background(), rec.RecordID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Nonce != reissued.Nonce {
		t.Error("reissued nonce was not persisted")
	}
}

func TestReissueRecordRejectsNonPending(t *testing.T) {
	_, profile := mustProfileAndDigest(t)
	auditorKey, _ := crypto.GenerateKey()
	svc, records := newTestService(t, auditorKey, common.Address{}, common.Address{})

	req := CreateRecordRequest{
		RecordID: "GOLD-BAR-SIGNED", Asset: json.RawMessage(`{"serialNumber":"8","weightGrams":"100","purity":"999.9"}`),
		Amount: "100000000000000000000",
	}
	rec, err := svc.CreateRecord(context.Background(), profile, profile.ProjectID, req)
	if err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	rec.Status = models.RecordStatusMinted
	if err := records.Update(context.Background(), rec); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := svc.ReissueRecord(context.Background(), rec.RecordID); !errors.Is(err, ErrRecordNotReissuable) {
		t.Errorf("ReissueRecord on Minted = %v, want ErrRecordNotReissuable", err)
	}

	if _, err := svc.ReissueRecord(context.Background(), "NO-SUCH-RECORD"); !errors.Is(err, ErrRecordNotFound) {
		t.Errorf("ReissueRecord on missing = %v, want ErrRecordNotFound", err)
	}
}

func TestCreateRecordRejectsInvalidAsset(t *testing.T) {
	_, profile := mustProfileAndDigest(t)
	auditorKey, _ := crypto.GenerateKey()
	svc, _ := newTestService(t, auditorKey, common.Address{}, common.Address{})

	req := CreateRecordRequest{
		RecordID: "BAD-1", Asset: json.RawMessage(`{"serialNumber":"1"}`),
		Amount: "1000",
	}
	if _, err := svc.CreateRecord(context.Background(), profile, profile.ProjectID, req); err == nil {
		t.Fatal("expected error for asset missing required fields")
	}
}

func mustProfileAndDigest(t *testing.T) ([32]byte, *Profile) {
	t.Helper()
	result, profile := ValidateProfile([]byte(goldGramProfile))
	if !result.Valid {
		t.Fatalf("profile fixture invalid: %v", result.Errors)
	}
	return profile.Digest, profile
}

// TestCreateRecordReservesRecordBeforePublish checks that a duplicate create
// for an existing RecordID (even with different metadata) must fail at
// the create-once record reservation and never reach the publish step, so it
// cannot overwrite the durable-publication row bound to the original record.
func TestCreateRecordReservesRecordBeforePublish(t *testing.T) {
	_, profile := mustProfileAndDigest(t)
	auditorKey, _ := crypto.GenerateKey()
	auditor := crypto.PubkeyToAddress(auditorKey.PublicKey)
	controllerAddr := common.HexToAddress("0x0000000000000000000000000000000000C0DE")
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000FADE")

	records := memory.NewAssetRecordRepository()
	packages := memory.NewAuditPackageRepository()
	attestations := memory.NewAttestationRepository()
	domainCfg := eip712.Domain{Name: "RWA-Supply-Attestation", Version: "1", ChainID: big.NewInt(31337), VerifyingContract: controllerAddr}
	ipfsStub := &stubDurabilityIPFS{published: map[string]bool{}}
	svc := NewRecordService(records, packages, attestations, ipfsStub, controllerAddr, vaultAddr, domainCfg, auditor)

	req1 := CreateRecordRequest{RecordID: "DUP-1", Asset: json.RawMessage(`{"serialNumber":"1","weightGrams":"1","purity":"999"}`), Amount: "1"}
	if _, err := svc.CreateRecord(context.Background(), profile, profile.ProjectID, req1); err != nil {
		t.Fatal(err)
	}
	if ipfsStub.calls != 1 {
		t.Fatalf("expected exactly one publish for the first create, got %d", ipfsStub.calls)
	}

	req2 := CreateRecordRequest{RecordID: "DUP-1", Asset: json.RawMessage(`{"serialNumber":"2","weightGrams":"2","purity":"999"}`), Amount: "1"}
	if _, err := svc.CreateRecord(context.Background(), profile, profile.ProjectID, req2); err == nil {
		t.Fatal("expected duplicate create to be rejected at record reservation")
	}
	if ipfsStub.calls != 1 {
		t.Fatalf("publish must NOT run for a duplicate create; call count = %d", ipfsStub.calls)
	}
}
