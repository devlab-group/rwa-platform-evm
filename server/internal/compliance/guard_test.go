package compliance

import (
	"context"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

const (
	wA     = "0x00000000000000000000000000000000000000A1"
	wB     = "0x00000000000000000000000000000000000000B2"
	wC     = "0x00000000000000000000000000000000000000C3"
	wVault = "0x00000000000000000000000000000000000000FA"
)

func allowedInvestor(t *testing.T, repos *repository.Repositories, addr string, validUntil int64) {
	t.Helper()
	if err := repos.Investors.Upsert(context.Background(), &models.Investor{Address: addr, Status: models.ComplianceAllowed, ValidUntil: validUntil}); err != nil {
		t.Fatal(err)
	}
}

func TestGuardCap(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	repos := memory.New()
	g := NewGuard(repos, 2, []string{wVault}, nil)
	allowedInvestor(t, repos, wA, 0)
	allowedInvestor(t, repos, wVault, 0)                      // system address never counts
	allowedInvestor(t, repos, wB, now.Add(-time.Hour).Unix()) // expired does not count

	if err := g.CheckAllow(ctx, wC, now); err != nil {
		t.Fatalf("one slot free: %v", err)
	}
	allowedInvestor(t, repos, wB, 0)
	if err := g.CheckAllow(ctx, wC, now); !errors.Is(err, ErrHolderCapReached) {
		t.Fatalf("at cap: got %v, want ErrHolderCapReached", err)
	}
	if err := g.CheckAllow(ctx, wA, now); err != nil {
		t.Fatalf("renewal of a counted wallet must pass: %v", err)
	}
	if err := g.CheckAllow(ctx, wVault, now); err != nil {
		t.Fatalf("system address is exempt: %v", err)
	}
	if err := NewGuard(repos, 0, nil, nil).CheckAllow(ctx, wC, now); err != nil {
		t.Fatalf("cap 0 means no cap: %v", err)
	}
}

func TestGuardCountsInFlightAllow(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	repos := memory.New()
	g := NewGuard(repos, 1, nil, nil)
	if err := repos.Transactions.Create(ctx, &models.Transaction{ID: "tx1", Status: models.TxPending, SubmittedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repos.ComplianceOperations.Create(ctx, &models.ComplianceOperation{ID: "tx1", Address: wA, Status: models.ComplianceAllowed, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := g.CheckAllow(ctx, wB, now); !errors.Is(err, ErrHolderCapReached) {
		t.Fatalf("in-flight allow must count: got %v", err)
	}
	if err := g.CheckAllow(ctx, wA, now); err != nil {
		t.Fatalf("the in-flight wallet itself must pass: %v", err)
	}

	// A failed transaction frees the slot
	tx, _ := repos.Transactions.Get(ctx, "tx1")
	tx.Status = models.TxFailed
	if err := repos.Transactions.Update(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := g.CheckAllow(ctx, wB, now); err != nil {
		t.Fatalf("failed tx must not count: %v", err)
	}
}

func TestGuardCountsInFlightWebhookAllow(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	repos := memory.New()
	g := NewGuard(repos, 1, nil, nil)
	if err := repos.Transactions.Create(ctx, &models.Transaction{ID: "tx1", Status: models.TxPending, SubmittedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repos.KYCEvents.Create(ctx, &models.KYCEvent{ID: "e1", Address: wA, Provider: "p", EventID: "e1", Outcome: "Allowed",
		ApplyStatus: models.KYCApplyApplying, TxID: "tx1", PayloadHash: "h1"}); err != nil {
		t.Fatal(err)
	}
	if err := g.CheckAllow(ctx, wB, now); !errors.Is(err, ErrHolderCapReached) {
		t.Fatalf("in-flight webhook allow must count: got %v", err)
	}
}

func TestParseSanctionsList(t *testing.T) {
	got, err := ParseSanctionsList("# header\n\n  " + wA + "  \n0X" + wB[2:] + "\n")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"not-an-address\n", wA[2:] + "\n", wA + " # trailing\n", "0x1234\n"} {
		if _, err := ParseSanctionsList(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func writeList(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestSanctionsListReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "list.txt")
	t0 := time.Now().Add(-time.Hour)
	writeList(t, path, wA+"\n", t0)
	l := NewSanctionsList(path)
	if changed, err := l.Reload(); !changed || err != nil {
		t.Fatalf("initial load: %v %v", changed, err)
	}
	if !l.Contains(wA) || !l.Contains("0x00000000000000000000000000000000000000a1") || l.Contains(wB) {
		t.Fatal("list content wrong")
	}
	if changed, _ := l.Reload(); changed {
		t.Fatal("unchanged file must not reload")
	}

	// Parse failure preserves the list; retry requires a changed modification time
	writeList(t, path, wB+"\ngarbage\n", t0.Add(time.Minute))
	if changed, err := l.Reload(); changed || err == nil {
		t.Fatalf("bad file: %v %v", changed, err)
	}
	if !l.Contains(wA) || l.Contains(wB) {
		t.Fatal("bad file replaced the list in force")
	}
	if _, err := l.Reload(); err != nil {
		t.Fatalf("same bad file must not error again: %v", err)
	}

	writeList(t, path, wB+"\n", t0.Add(2*time.Minute))
	if changed, err := l.Reload(); !changed || err != nil {
		t.Fatalf("fixed file: %v %v", changed, err)
	}
	if l.Contains(wA) || !l.Contains(wB) {
		t.Fatal("reload did not swap the list")
	}
}

func TestGuardRefusesSanctionedAndReportsAllowed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "list.txt")
	writeList(t, path, wA+"\n", time.Now())
	l := NewSanctionsList(path)
	if _, err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	repos := memory.New()
	g := NewGuard(repos, 0, nil, l)
	if err := g.CheckAllow(ctx, wA, time.Now()); !errors.Is(err, ErrSanctionedAddress) {
		t.Fatalf("got %v, want ErrSanctionedAddress", err)
	}
	if err := g.CheckAllow(ctx, wB, time.Now()); err != nil {
		t.Fatal(err)
	}
	allowedInvestor(t, repos, wA, 0)
	allowedInvestor(t, repos, wB, 0)
	got, err := g.SanctionedAllowed(ctx, time.Now())
	if err != nil || len(got) != 1 || got[0] != wA {
		t.Fatalf("got %v, %v", got, err)
	}
}

type raised struct{ kind, address string }

func guardedReconciler(t *testing.T, repos *repository.Repositories, g *Guard) (*WebhookReconciler, *[]raised) {
	t.Helper()
	txs := blockchain.NewTxManager(blockchain.NewFakeClient(), repos.Transactions, big.NewInt(31337), blockchain.FeeModeLegacy)
	priv, _ := crypto.GenerateKey()
	r := NewWebhookReconciler(repos.KYCEvents, repos.Transactions,
		NewStatusService(txs, common.HexToAddress("0x00000000000000000000000000000000C0A1"), blockchain.NewStaticKeySigner(priv)))
	var got []raised
	r.SetGuard(g, func(_ context.Context, kind, address, _ string) { got = append(got, raised{kind, address}) })
	return r, &got
}

func seedEvent(t *testing.T, repos *repository.Repositories, id, addr, outcome string) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC()
	if _, err := repos.KYCEvents.ClaimLatestForAddress(ctx, addr, at, kycEventIDKey("p", id)); err != nil {
		t.Fatal(err)
	}
	if err := repos.KYCEvents.Create(ctx, &models.KYCEvent{ID: id, Address: addr, Provider: "p", EventID: id, Status: outcome, Outcome: outcome,
		ApplyStatus: models.KYCApplyAccepted, OccurredAt: at, ReceivedAt: at, PayloadHash: id}); err != nil {
		t.Fatal(err)
	}
}

func eventStatus(t *testing.T, repos *repository.Repositories, id string) models.KYCApplyStatus {
	t.Helper()
	all, _ := repos.KYCEvents.List(context.Background())
	for _, e := range all {
		if e.ID == id {
			return e.ApplyStatus
		}
	}
	t.Fatalf("event %s missing", id)
	return ""
}

func TestReconcilerRefusesApprovalAtCapButRelaysBlock(t *testing.T) {
	ctx := context.Background()
	repos := memory.New()
	allowedInvestor(t, repos, wA, 0)
	r, raisedAlerts := guardedReconciler(t, repos, NewGuard(repos, 1, nil, nil))
	seedEvent(t, repos, "allow-b", wB, "Allowed")
	seedEvent(t, repos, "block-c", wC, "Blocked")

	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := eventStatus(t, repos, "allow-b"); got != models.KYCApplyFailed {
		t.Fatalf("approval at cap: %s, want terminal Failed", got)
	}
	if got := eventStatus(t, repos, "block-c"); got != models.KYCApplyApplying {
		t.Fatalf("block at cap: %s, want Applying", got)
	}
	if len(*raisedAlerts) != 1 || (*raisedAlerts)[0] != (raised{AlertHolderCapReached, wB}) {
		t.Fatalf("alerts = %v", *raisedAlerts)
	}
	// The terminal event is not retried or re-alerted
	if err := r.Reconcile(ctx); err != nil || len(*raisedAlerts) != 1 {
		t.Fatalf("second pass: %v %v", err, *raisedAlerts)
	}
}

func TestReconcilerRefusesSanctionedApproval(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "list.txt")
	writeList(t, path, wB+"\n", time.Now())
	l := NewSanctionsList(path)
	_, _ = l.Reload()
	repos := memory.New()
	r, raisedAlerts := guardedReconciler(t, repos, NewGuard(repos, 0, nil, l))
	seedEvent(t, repos, "allow-b", wB, "Allowed")
	seedEvent(t, repos, "allow-a", wA, "Allowed")

	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := eventStatus(t, repos, "allow-b"); got != models.KYCApplyFailed {
		t.Fatalf("listed approval: %s, want Failed", got)
	}
	if got := eventStatus(t, repos, "allow-a"); got != models.KYCApplyApplying {
		t.Fatalf("unlisted approval: %s, want Applying", got)
	}
	if len(*raisedAlerts) != 1 || (*raisedAlerts)[0] != (raised{AlertSanctionsMatch, wB}) {
		t.Fatalf("alerts = %v", *raisedAlerts)
	}
}

// An unindexed Allowed transaction releases its slot after inFlightWindow
func TestGuard_InFlightAllowOlderThanWindow_StopsCounting(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		submitted time.Time
		wantErr   error
	}{
		{name: "inside the window counts", submitted: now.Add(-59 * time.Minute), wantErr: ErrHolderCapReached},
		{name: "older than the window is ignored", submitted: now.Add(-61 * time.Minute), wantErr: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repos := memory.New()
			g := NewGuard(repos, 1, nil, nil)
			if err := repos.Transactions.Create(ctx, &models.Transaction{ID: "tx1", Status: models.TxPending, SubmittedAt: tt.submitted}); err != nil {
				t.Fatal(err)
			}
			if err := repos.ComplianceOperations.Create(ctx, &models.ComplianceOperation{ID: "tx1", Address: wA, Status: models.ComplianceAllowed, CreatedAt: tt.submitted}); err != nil {
				t.Fatal(err)
			}
			if err := g.CheckAllow(ctx, wB, now); !errors.Is(err, tt.wantErr) {
				t.Fatalf("CheckAllow with an allow submitted at %s = %v, want %v", tt.submitted, err, tt.wantErr)
			}
		})
	}
}

func TestGuard_InFlightCounting_SkipsCountedSystemAndUnknownTransactions(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		maxWallets int
		allowed    []string
		opAddress  string
		createTx   bool
	}{
		{name: "a renewal in flight for an Allowed wallet counts once", maxWallets: 2, allowed: []string{wA}, opAddress: wA, createTx: true},
		{name: "an in-flight allow for a system address never counts", maxWallets: 1, opAddress: wVault, createTx: true},
		{name: "an operation without a transaction record does not count", maxWallets: 1, opAddress: wA, createTx: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repos := memory.New()
			g := NewGuard(repos, tt.maxWallets, []string{wVault}, nil)
			for _, a := range tt.allowed {
				allowedInvestor(t, repos, a, 0)
			}
			if tt.createTx {
				if err := repos.Transactions.Create(ctx, &models.Transaction{ID: "tx1", Status: models.TxPending, SubmittedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			if err := repos.ComplianceOperations.Create(ctx, &models.ComplianceOperation{ID: "tx1", Address: tt.opAddress, Status: models.ComplianceAllowed, CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := g.CheckAllow(ctx, wB, now); err != nil {
				t.Fatalf("CheckAllow(%s) with cap %d = %v, want nil (one slot still free)", wB, tt.maxWallets, err)
			}
		})
	}
}

// failingInvestors fails List, so the guard cannot count wallets
type failingInvestors struct{ repository.InvestorRepository }

func (failingInvestors) List(context.Context) ([]*models.Investor, error) {
	return nil, errors.New("read model unavailable")
}

// Guard read errors leave approvals retryable rather than Failed
func TestReconciler_GuardReadError_LeavesApprovalRetryable(t *testing.T) {
	ctx := context.Background()
	repos := memory.New()
	g := NewGuard(&repository.Repositories{Investors: failingInvestors{repos.Investors},
		ComplianceOperations: repos.ComplianceOperations, KYCEvents: repos.KYCEvents, Transactions: repos.Transactions}, 1, nil, nil)
	r, raisedAlerts := guardedReconciler(t, repos, g)
	seedEvent(t, repos, "allow-b", wB, "Allowed")

	err := r.Reconcile(ctx)
	t.Logf("Reconcile returned %v", err)

	if got := eventStatus(t, repos, "allow-b"); got != models.KYCApplyAccepted {
		t.Fatalf("approval after a guard read error: %s, want it still Accepted for a retry", got)
	}
	if len(*raisedAlerts) != 0 {
		t.Fatalf("alerts = %v, want none for a read error", *raisedAlerts)
	}
}

func TestSanctionsListReload_MissingFile_KeepsListInForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "list.txt")
	writeList(t, path, wA+"\n", time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	l := NewSanctionsList(path)
	if changed, err := l.Reload(); err != nil || !changed {
		t.Fatalf("first Reload = (%v, %v), want (true, nil)", changed, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if changed, err := l.Reload(); err == nil || changed {
		t.Fatalf("Reload of a missing file = (%v, %v), want (false, error)", changed, err)
	}
	if !l.Contains(wA) {
		t.Fatalf("Contains(%s) after a failed reload = false, want the previous list kept", wA)
	}
}
