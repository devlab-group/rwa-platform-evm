package assets

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/auditpkg"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/eip712"
)

const burnTestRecordAmount = "1000"

// newBurnTestEnv provides a Minted record with burnTestRecordAmount
func newBurnTestEnv(t *testing.T) (*BurnService, *Profile, *models.AssetRecord, *memory.BurnRepository, *memory.AssetRecordRepository) {
	t.Helper()
	_, profile := mustProfileAndDigest(t)
	auditorKey, _ := crypto.GenerateKey()
	controllerAddr := common.HexToAddress("0x0000000000000000000000000000000000C0DE")
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000FADE")
	rs, records := newTestService(t, auditorKey, controllerAddr, vaultAddr)
	rec, err := rs.CreateRecord(context.Background(), profile, profile.ProjectID, CreateRecordRequest{
		RecordID: "GOLD-BURN-1", Asset: json.RawMessage(`{"serialNumber":"1","weightGrams":"100","purity":"999.9"}`),
		Amount: burnTestRecordAmount,
		Proofs: []ProofRecord{{Type: "assay", SHA256: repeat("a", 64)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec.Status = models.RecordStatusMinted
	if err := records.Update(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	burns := memory.NewBurnRepository()
	return NewBurnService(rs, burns), profile, rec, burns, records
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

func createBurn(t *testing.T, svc *BurnService, p *Profile, amount string) (*models.Burn, error) {
	t.Helper()
	return svc.CreateBurn(context.Background(), p, p.ProjectID, CreateBurnRequest{RecordID: "GOLD-BURN-1", Amount: amount})
}

func TestCreateBurnBuildsVerifiablePackage(t *testing.T) {
	svc, profile, rec, _, _ := newBurnTestEnv(t)
	proofs := []ProofRecord{{Type: "weigh-in", SHA256: repeat("b", 64)}}
	burn, err := svc.CreateBurn(context.Background(), profile, profile.ProjectID, CreateBurnRequest{RecordID: rec.RecordID, Amount: "400", Proofs: proofs})
	if err != nil {
		t.Fatal(err)
	}
	if burn.Status != models.BurnStatusPending || burn.Amount != "400" || burn.RecordID != rec.RecordID || burn.Unit != rec.Unit {
		t.Fatalf("unexpected burn %+v", burn)
	}
	if want := crypto.Keccak256Hash([]byte(burn.BurnID)).Hex(); burn.OperationID != want {
		t.Errorf("operationId = %s, want keccak256(burnId) %s", burn.OperationID, want)
	}
	if burn.ValidUntil <= time.Now().Unix() {
		t.Errorf("validUntil %d not in the future", burn.ValidUntil)
	}

	// metadata is the record's envelope with the burn's amount and proofs
	var meta struct {
		RecordID  string          `json:"recordId"`
		ProjectID string          `json:"projectId"`
		Asset     json.RawMessage `json:"asset"`
		Issuance  struct{ Amount, Unit string }
		Proofs    []ProofRecord `json:"proofs"`
		CreatedAt string
	}
	if err := json.Unmarshal(burn.MetadataRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Issuance.Amount != "400" || meta.Issuance.Unit != rec.Unit || meta.RecordID != rec.RecordID || meta.ProjectID != rec.ProjectID {
		t.Errorf("burn metadata envelope wrong: %+v", meta)
	}
	if len(meta.Proofs) != 1 || meta.Proofs[0] != proofs[0] {
		t.Errorf("burn metadata proofs = %+v, want the burn's own proofs", meta.Proofs)
	}
	var recMeta, burnMeta struct{ Asset json.RawMessage }
	_ = json.Unmarshal(rec.MetadataRaw, &recMeta)
	_ = json.Unmarshal(burn.MetadataRaw, &burnMeta)
	if string(recMeta.Asset) != string(burnMeta.Asset) {
		t.Errorf("asset changed: %s vs %s", recMeta.Asset, burnMeta.Asset)
	}

	zipBytes, err := svc.BuildPackage(burn, profile.Digest, profile.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := auditpkg.OpenPackage(zipBytes)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Manifest.PrimaryType != "BurnAttestation" {
		t.Errorf("primaryType = %q", opened.Manifest.PrimaryType)
	}
	var td auditpkg.TypedDataDoc
	if err := json.Unmarshal(opened.Files["typed-data.json"], &td); err != nil {
		t.Fatal(err)
	}
	m := td.Message
	if td.PrimaryType != "BurnAttestation" || m.OperationID != burn.OperationID || m.Amount != "400" ||
		m.MetadataDigest != burn.MetadataDigest || m.Nonce != burn.Nonce || m.RecordKey != "" {
		t.Errorf("typed data wrong: %+v", td)
	}
	if string(opened.Files["metadata.json"]) != string(burn.MetadataRaw) {
		t.Error("package metadata is not the burn's canonical metadata")
	}

	// Verify a signature using the burn typed data
	var opID, mdDigest [32]byte
	copy(opID[:], common.FromHex(m.OperationID))
	copy(mdDigest[:], common.FromHex(m.MetadataDigest))
	nonce, _ := new(big.Int).SetString(m.Nonce, 10)
	att := eip712.BurnAttestation{
		Auditor: common.HexToAddress(m.Auditor), ProfileDigest: profile.Digest, OperationID: opID, MetadataDigest: mdDigest,
		Amount: big.NewInt(400), Nonce: nonce, ValidUntil: uint64(m.ValidUntil), Vault: common.HexToAddress(m.Vault),
	}
	key, _ := crypto.GenerateKey()
	att.Auditor = crypto.PubkeyToAddress(key.PublicKey)
	dom := svc.records.eip712Domain
	digest := eip712.BurnDigest(dom, att)
	sig, _ := crypto.Sign(digest[:], key)
	sig[64] += 27
	sr := auditpkg.SignedResult{
		FormatVersion: "1.0", Auditor: att.Auditor.Hex(), PrimaryType: "BurnAttestation",
		TypedDataDigest: "0x" + common.Bytes2Hex(digest[:]), Signature: "0x" + common.Bytes2Hex(sig),
	}
	if err := auditpkg.VerifyBurnSignedResult(sr, dom, att, att.Auditor); err != nil {
		t.Errorf("VerifyBurnSignedResult: %v", err)
	}
}

func TestCreateBurnOmitsProofsKeyWhenNone(t *testing.T) {
	svc, profile, _, _, _ := newBurnTestEnv(t)
	burn, err := createBurn(t, svc, profile, "1")
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(burn.MetadataRaw, &top)
	if _, ok := top["proofs"]; ok {
		t.Error("burn without proofs must not inherit the record's proofs")
	}
}

func TestCreateBurnRefusals(t *testing.T) {
	svc, profile, rec, _, records := newBurnTestEnv(t)
	for _, amount := range []string{"0", "-5", "1.5", "abc", "", "0x10", "+5"} {
		if _, err := createBurn(t, svc, profile, amount); !errors.Is(err, ErrBurnInvalid) {
			t.Errorf("amount %q: err = %v, want ErrBurnInvalid", amount, err)
		}
	}
	if _, err := createBurn(t, svc, profile, "1001"); !errors.Is(err, ErrBurnExceedsRemaining) {
		t.Errorf("over remaining: err = %v", err)
	}
	if _, err := svc.CreateBurn(context.Background(), profile, profile.ProjectID, CreateBurnRequest{RecordID: "NOPE", Amount: "1"}); !errors.Is(err, ErrRecordNotFound) {
		t.Errorf("unknown record: err = %v", err)
	}
	rec.Status = models.RecordStatusSigned
	if err := records.Update(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if _, err := createBurn(t, svc, profile, "1"); !errors.Is(err, ErrBurnRecordNotMinted) {
		t.Errorf("non-Minted: err = %v", err)
	}
}

func TestRemainingCountsReservedAndBurnedNotExpired(t *testing.T) {
	svc, profile, rec, burns, _ := newBurnTestEnv(t)
	ctx := context.Background()

	pending, err := createBurn(t, svc, profile, "300")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createBurn(t, svc, profile, "701"); !errors.Is(err, ErrBurnExceedsRemaining) {
		t.Fatalf("pending burn must reserve: err = %v", err)
	}

	// expired Pending burns stop reserving
	pending.ValidUntil = time.Now().Add(-time.Minute).Unix()
	if ok, err := burns.UpdateConditional(ctx, pending, pending.Version); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if rem, _ := svc.Remaining(ctx, rec, ""); rem.String() != "1000" {
		t.Errorf("remaining with an expired Pending = %s, want 1000", rem)
	}

	// Burned burns count even past validUntil
	pending, _ = burns.Get(ctx, pending.BurnID)
	pending.Status = models.BurnStatusBurned
	if ok, err := burns.UpdateConditional(ctx, pending, pending.Version); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if rem, _ := svc.Remaining(ctx, rec, ""); rem.String() != "700" {
		t.Errorf("remaining with a Burned = %s, want 700", rem)
	}
	if _, err := createBurn(t, svc, profile, "700"); err != nil {
		t.Errorf("exact remaining must fit: %v", err)
	}
}

func TestCreateBurnConcurrentLastAmount(t *testing.T) {
	svc, profile, _, burns, _ := newBurnTestEnv(t)
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := createBurn(t, svc, profile, burnTestRecordAmount)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrBurnExceedsRemaining) {
			t.Errorf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent creates for the last amount succeeded, want exactly 1", ok)
	}
	if got, _ := burns.ListByRecord(context.Background(), "GOLD-BURN-1"); len(got) != 1 {
		t.Errorf("%d burns stored, want 1", len(got))
	}
}

func TestReissueBurn(t *testing.T) {
	svc, profile, _, burns, _ := newBurnTestEnv(t)
	ctx := context.Background()
	burn, err := createBurn(t, svc, profile, "600")
	if err != nil {
		t.Fatal(err)
	}
	// Reissue must exclude the burn's own reservation
	if _, err := svc.ReissueBurn(ctx, burn.BurnID); err != nil {
		t.Fatalf("reissue of a live burn: %v", err)
	}
	burn, _ = burns.Get(ctx, burn.BurnID)
	// Expire the burn before refreshing its nonce and expiry
	burn.ValidUntil = time.Now().Add(-time.Hour).Unix()
	if ok, err := burns.UpdateConditional(ctx, burn, burn.Version); err != nil || !ok {
		t.Fatal(err, ok)
	}
	old, _ := burns.Get(ctx, burn.BurnID)

	re, err := svc.ReissueBurn(ctx, burn.BurnID)
	if err != nil {
		t.Fatal(err)
	}
	if re.Nonce == old.Nonce || re.ValidUntil <= time.Now().Unix() {
		t.Errorf("reissue must rotate nonce and validUntil: %+v", re)
	}
	if re.BurnID != old.BurnID || re.OperationID != old.OperationID || re.MetadataDigest != old.MetadataDigest || re.Amount != old.Amount || string(re.MetadataRaw) != string(old.MetadataRaw) {
		t.Errorf("reissue changed identity: %+v vs %+v", re, old)
	}
	if stored, _ := burns.Get(ctx, burn.BurnID); stored.Nonce != re.Nonce {
		t.Error("reissued nonce not persisted")
	}

	// Expire 600, reserve 500 elsewhere, then reject reissue against the remaining 500
	re.ValidUntil = time.Now().Add(-time.Hour).Unix()
	if ok, _ := burns.UpdateConditional(ctx, re, re.Version); !ok {
		t.Fatal("update failed")
	}
	if _, err := createBurn(t, svc, profile, "500"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReissueBurn(ctx, burn.BurnID); !errors.Is(err, ErrBurnExceedsRemaining) {
		t.Errorf("reissue that no longer fits: err = %v", err)
	}

	// Burned burns cannot be reissued
	b, _ := burns.Get(ctx, burn.BurnID)
	b.Status = models.BurnStatusBurned
	if ok, _ := burns.UpdateConditional(ctx, b, b.Version); !ok {
		t.Fatal("update failed")
	}
	if _, err := svc.ReissueBurn(ctx, burn.BurnID); !errors.Is(err, ErrBurnAlreadyBurned) {
		t.Errorf("reissue of Burned: err = %v", err)
	}
	if _, err := svc.ReissueBurn(ctx, "missing"); !errors.Is(err, ErrBurnNotFound) {
		t.Errorf("reissue of unknown: err = %v", err)
	}
}

func TestReconcileBurned(t *testing.T) {
	svc, profile, _, burns, _ := newBurnTestEnv(t)
	ctx := context.Background()
	events := memory.NewChainEventRepository()
	controller := "0x0000000000000000000000000000000000C0DE"

	live, _ := createBurn(t, svc, profile, "100")
	expired, _ := createBurn(t, svc, profile, "100")
	expired.ValidUntil = time.Now().Add(-time.Hour).Unix()
	if ok, _ := burns.UpdateConditional(ctx, expired, expired.Version); !ok {
		t.Fatal("update failed")
	}
	other, _ := createBurn(t, svc, profile, "100")

	for i, e := range []struct{ op, tx string }{{live.OperationID, "0xaaa"}, {expired.OperationID, "0xbbb"}, {"0x" + repeat("9", 64), "0xccc"}} {
		if err := events.Create(ctx, &models.ChainEvent{
			ChainID: 31337, Address: controller, Name: "Burned", TxHash: e.tx, LogIndex: uint(i), BlockNumber: 10,
			Data: map[string]any{"operationId": e.op, "amount": "100"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 { // repeated runs are safe
		if err := svc.ReconcileBurned(ctx, events, 31337, controller); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		id, tx string
		st     models.BurnStatus
	}{{live.BurnID, "0xaaa", models.BurnStatusBurned}, {expired.BurnID, "0xbbb", models.BurnStatusBurned}, {other.BurnID, "", models.BurnStatusPending}} {
		got, _ := burns.Get(ctx, c.id)
		if got.Status != c.st || got.BurnTxHash != c.tx {
			t.Errorf("burn %s = %s/%q, want %s/%q", c.id, got.Status, got.BurnTxHash, c.st, c.tx)
		}
	}
}
