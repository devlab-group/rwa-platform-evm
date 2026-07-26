package main

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/keys"
)

func init() { gin.SetMode(gin.TestMode) }

// TestRouterHandlerSwapIsAtomic is the regression test for
// routerHandler: requests served concurrently with a Store must always see
// one complete engine's response, never a nil pointer or a partially
// constructed one (the whole point of using atomic.Pointer instead of a
// bare field).
func TestRouterHandlerSwapIsAtomic(t *testing.T) {
	h := &routerHandler{}
	r1 := gin.New()
	r1.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "v1") })
	h.set(r1)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "v1" {
		t.Fatalf("before swap: status=%d body=%q", w.Code, w.Body.String())
	}

	r2 := gin.New()
	r2.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "v2") })
	h.set(r2)

	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "v2" {
		t.Fatalf("after swap: status=%d body=%q", w.Code, w.Body.String())
	}
}

// fakeKeyProvider is a minimal keys.Provider test double that counts Close calls.
type fakeKeyProvider struct {
	mu     sync.Mutex
	closed int
}

func (f *fakeKeyProvider) Address(ctx context.Context) (common.Address, error) {
	return common.Address{}, nil
}
func (f *fakeKeyProvider) SignTx(ctx context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
	return tx, nil
}
func (f *fakeKeyProvider) Reload(ctx context.Context) error { return nil }
func (f *fakeKeyProvider) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

var _ keys.Provider = (*fakeKeyProvider)(nil)

// TestProviderRegistryTracksAllGenerations is the regression test for
// providerRegistry: a provider set added by an initial buildApp call AND a
// later watchForActivation rebuild must BOTH be closed at shutdown, not
// just whichever set was added last.
func TestProviderRegistryTracksAllGenerations(t *testing.T) {
	reg := &providerRegistry{}
	gen1 := &fakeKeyProvider{}
	gen2 := &fakeKeyProvider{}
	reg.add(nil) // no-op sanity: adding an empty/nil slice must not panic
	reg.add([]keys.Provider{gen1})
	reg.add([]keys.Provider{gen2})

	reg.closeAll()
	if gen1.closed != 1 {
		t.Errorf("gen1.closed = %d, want 1", gen1.closed)
	}
	if gen2.closed != 1 {
		t.Errorf("gen2.closed = %d, want 1", gen2.closed)
	}
}

// TestAddressesFromProject: the deployed contract set + auditor are sourced
// directly from the DB Project record (config is bootstrap-only now).
func TestAddressesFromProject(t *testing.T) {
	p := &models.Project{
		Addresses: models.Addresses{
			Token: "0xT001", Compliance: "0xC001", SupplyController: "0xS001",
			Vault: "0xV001", RedemptionEscrow: "0xE001", Strategy: "0xST01", QuoteToken: "0xQ001",
		},
		Auditor: "0xAUD1",
	}
	gotAddrs, gotAuditor := addressesFromProject(p)
	if gotAddrs != p.Addresses {
		t.Errorf("addresses = %+v, want %+v", gotAddrs, p.Addresses)
	}
	if gotAuditor != p.Auditor {
		t.Errorf("auditor = %q, want %q", gotAuditor, p.Auditor)
	}
}

// TestLoadProjectAddressesOnlyWhenActive: loadProjectAddresses sources
// addresses from the single Project record ONLY when it is Active — a
// missing record (factory-only boot) or a not-yet-Active project yields the
// zero set, leaving every address-dependent service gated until
// watchForActivation wires them post-activation.
func TestLoadProjectAddressesOnlyWhenActive(t *testing.T) {
	ctx := context.Background()

	// No record at all: zero set.
	repos := memory.New()
	if addrs, auditor := loadProjectAddresses(ctx, repos); addrs != (models.Addresses{}) || auditor != "" {
		t.Fatalf("no project: got addrs=%+v auditor=%q, want zero", addrs, auditor)
	}

	// A deploying (not-yet-Active) project with addresses already computed:
	// still gated (zero set) until it reaches Active.
	full := models.Addresses{
		Token: "0xT001", Compliance: "0xC001", SupplyController: "0xS001",
		Vault: "0xV001", RedemptionEscrow: "0xE001", Strategy: "0xST01", QuoteToken: "0xQ001",
	}
	repos = memory.New()
	if err := repos.Projects.Upsert(ctx, &models.Project{Status: models.ProjectStatusDeploying, Addresses: full, Auditor: "0xAUD1"}); err != nil {
		t.Fatal(err)
	}
	if addrs, _ := loadProjectAddresses(ctx, repos); addrs != (models.Addresses{}) {
		t.Fatalf("deploying project: got addrs=%+v, want zero (gated until Active)", addrs)
	}

	// Active project: full set + auditor.
	repos = memory.New()
	if err := repos.Projects.Upsert(ctx, &models.Project{Status: models.ProjectStatusActive, Addresses: full, Auditor: "0xAUD1"}); err != nil {
		t.Fatal(err)
	}
	addrs, auditor := loadProjectAddresses(ctx, repos)
	if addrs != full {
		t.Errorf("active project: addrs = %+v, want %+v", addrs, full)
	}
	if auditor != "0xAUD1" {
		t.Errorf("active project: auditor = %q, want 0xAUD1", auditor)
	}
}

// TestIndexerUnsafe is the regression test for the shared
// safety-state check backing both /readyz and runIndexDependentTicker: no
// checkpoint yet is NOT unsafe (normal at/just after startup), a normal
// checkpoint is not unsafe, and a ReconciliationRequired checkpoint is.
func TestIndexerUnsafe(t *testing.T) {
	repos := memory.New()
	ctx := context.Background()
	const chainID = 31337

	unsafe, err := indexerUnsafe(ctx, repos, chainID)
	if err != nil {
		t.Fatalf("no checkpoint yet: %v", err)
	}
	if unsafe {
		t.Fatal("expected NOT unsafe with no checkpoint yet")
	}

	if err := repos.IndexerCheckpoints.Set(ctx, &models.IndexerCheckpoint{
		ChainID: chainID, Address: indexer.CheckpointAddress, LastBlock: 100, LastBlockHash: "0xabc",
	}); err != nil {
		t.Fatal(err)
	}
	unsafe, err = indexerUnsafe(ctx, repos, chainID)
	if err != nil {
		t.Fatal(err)
	}
	if unsafe {
		t.Fatal("expected NOT unsafe for a normal checkpoint")
	}

	if err := repos.IndexerCheckpoints.Set(ctx, &models.IndexerCheckpoint{
		ChainID: chainID, Address: indexer.CheckpointAddress, LastBlock: 100, LastBlockHash: "0xabc",
		ReconciliationRequired: true,
	}); err != nil {
		t.Fatal(err)
	}
	unsafe, err = indexerUnsafe(ctx, repos, chainID)
	if err != nil {
		t.Fatal(err)
	}
	if !unsafe {
		t.Fatal("expected unsafe once ReconciliationRequired is set")
	}
}

// TestRunIndexDependentTickerSkipsWhileUnsafe proves the reconciler-gating
// wrapper never invokes fn while the indexer is ReconciliationRequired, and
// does invoke it once safe again.
func TestRunIndexDependentTickerSkipsWhileUnsafe(t *testing.T) {
	repos := memory.New()
	ctx, cancel := context.WithCancel(context.Background())
	const chainID = 31337
	if err := repos.IndexerCheckpoints.Set(ctx, &models.IndexerCheckpoint{
		ChainID: chainID, Address: indexer.CheckpointAddress, LastBlock: 100, LastBlockHash: "0xabc",
		ReconciliationRequired: true,
	}); err != nil {
		t.Fatal(err)
	}

	var calls int
	var mu sync.Mutex
	done := make(chan struct{})
	go runIndexDependentTicker(ctx, repos, config.Config{ChainID: chainID}, 5*time.Millisecond, "test", func() error {
		mu.Lock()
		calls++
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	})

	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	gotWhileUnsafe := calls
	mu.Unlock()
	if gotWhileUnsafe != 0 {
		t.Fatalf("expected fn never called while unsafe, got %d calls", gotWhileUnsafe)
	}

	if err := repos.IndexerCheckpoints.Set(ctx, &models.IndexerCheckpoint{
		ChainID: chainID, Address: indexer.CheckpointAddress, LastBlock: 100, LastBlockHash: "0xabc",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expected fn to be called once safe again")
	}
	cancel()
}

// TestRunAsReconcilerLeaderSerializesConcurrentReplicas checks that one
// reconciler leader runs per project/chain in multi-instance mode: while
// another replica (simulated by acquiring the
// same lease key directly under a different holder id) currently holds the
// reconciler's lease, fn must not run; once released, the next tick
// acquires and runs it, and releases the lease again afterward so a
// following tick (or another replica) can take over.
func TestRunAsReconcilerLeaderSerializesConcurrentReplicas(t *testing.T) {
	repos := memory.New()
	ctx := context.Background()
	cfg := config.Config{ChainID: 31337}

	token, ok, err := repos.NonceLeases.Acquire(ctx, "reconciler:test/leader:31337", "other-replica", time.Minute)
	if err != nil || !ok {
		t.Fatalf("simulated other replica failed to acquire: ok=%v err=%v", ok, err)
	}

	var calls int
	runAsReconcilerLeader(ctx, repos, cfg, "test/leader", func() { calls++ })
	if calls != 0 {
		t.Fatalf("expected fn NOT to run while another replica holds the lease, got %d calls", calls)
	}

	if err := repos.NonceLeases.Release(ctx, "reconciler:test/leader:31337", "other-replica", token); err != nil {
		t.Fatal(err)
	}

	runAsReconcilerLeader(ctx, repos, cfg, "test/leader", func() { calls++ })
	if calls != 1 {
		t.Fatalf("expected fn to run once the lease is free, got %d calls", calls)
	}

	// The lease must be released again after fn returns, so a subsequent
	// tick (this replica or another) can acquire it once more.
	_, ok, err = repos.NonceLeases.Acquire(ctx, "reconciler:test/leader:31337", "other-replica", time.Minute)
	if err != nil || !ok {
		t.Fatalf("expected the lease to be free again after runAsReconcilerLeader returned: ok=%v err=%v", ok, err)
	}
}
