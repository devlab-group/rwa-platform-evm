package redemption

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

func ev(name, id string, block uint64, logIndex uint, extra map[string]any) *models.ChainEvent {
	data := map[string]any{"id": id}
	for k, v := range extra {
		data[k] = v
	}
	return &models.ChainEvent{Name: name, BlockNumber: block, LogIndex: logIndex, TxHash: name + "-" + id, Data: data}
}

func TestBuildStatesRequestOnly(t *testing.T) {
	events := []*models.ChainEvent{
		ev("RedemptionRequested", "1", 10, 0, map[string]any{
			"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1000),
		}),
	}
	states := BuildStates(events, 1209600)
	r, ok := states["1"]
	if !ok {
		t.Fatal("expected state for id 1")
	}
	if r.Status != models.RedemptionPending {
		t.Errorf("Status = %s, want Pending", r.Status)
	}
	if r.TimeoutAt != 1000+1209600 {
		t.Errorf("TimeoutAt = %d", r.TimeoutAt)
	}
	if r.RWAAmount != "1000" || r.QuoteAmount != "950" {
		t.Errorf("amounts wrong: %+v", r)
	}
}

func TestBuildStatesFullLifecycleToCompleted(t *testing.T) {
	events := []*models.ChainEvent{
		ev("RedemptionRequested", "1", 10, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1000)}),
		ev("RedemptionFunded", "1", 12, 0, map[string]any{"funder": "0xBBB", "quoteAmount": "950"}),
		ev("RedemptionCompleted", "1", 15, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950"}),
	}
	states := BuildStates(events, 1209600)
	r := states["1"]
	if r.Status != models.RedemptionCompleted {
		t.Errorf("Status = %s, want Completed", r.Status)
	}
	if r.FundTxHash == "" || r.ClaimTxHash == "" || r.RequestTxHash == "" {
		t.Errorf("expected all tx hashes set: %+v", r)
	}
}

func TestBuildStatesRejectedPath(t *testing.T) {
	events := []*models.ChainEvent{
		ev("RedemptionRequested", "2", 10, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1000)}),
		ev("RedemptionRejected", "2", 11, 0, map[string]any{"reasonCode": "0xdead", "caller": "0xCCC"}),
	}
	states := BuildStates(events, 1209600)
	r := states["2"]
	if r.Status != models.RedemptionRejected {
		t.Errorf("Status = %s, want Rejected", r.Status)
	}
	if r.ReasonCode != "0xdead" {
		t.Errorf("ReasonCode = %q", r.ReasonCode)
	}
}

func TestBuildStatesCancelledPath(t *testing.T) {
	events := []*models.ChainEvent{
		ev("RedemptionRequested", "3", 10, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1000)}),
		ev("RedemptionCancelled", "3", 20, 0, map[string]any{"beneficiary": "0xAAA"}),
	}
	states := BuildStates(events, 1209600)
	if states["3"].Status != models.RedemptionCancelled {
		t.Errorf("Status = %s, want Cancelled", states["3"].Status)
	}
}

func TestBuildStatesIgnoresOutOfOrderTransitions(t *testing.T) {
	// A Funded event for an id that was never Requested (or is already
	// terminal) must not fabricate/overwrite state — this can only arise
	// from a bug or malformed fixture, and the projector must be robust to
	// it rather than panicking or corrupting the read model.
	events := []*models.ChainEvent{
		ev("RedemptionFunded", "4", 5, 0, map[string]any{"funder": "0xBBB", "quoteAmount": "1"}),
	}
	states := BuildStates(events, 1209600)
	if _, ok := states["4"]; ok {
		t.Errorf("expected no state for an id that was never requested")
	}
}

func TestBuildStatesAppliesEventsInBlockOrderRegardlessOfInputOrder(t *testing.T) {
	// Feed events out of chronological order; BuildStates must sort by
	// (blockNumber, logIndex) before replaying, not trust input order.
	events := []*models.ChainEvent{
		ev("RedemptionCompleted", "5", 30, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950"}),
		ev("RedemptionRequested", "5", 10, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1000)}),
		ev("RedemptionFunded", "5", 20, 0, map[string]any{"funder": "0xBBB", "quoteAmount": "950"}),
	}
	states := BuildStates(events, 1209600)
	if states["5"].Status != models.RedemptionCompleted {
		t.Errorf("Status = %s, want Completed", states["5"].Status)
	}
}

func TestBuildStatesRejectedCannotBeReFunded(t *testing.T) {
	// Once Rejected (terminal), a stray/duplicate Funded event for the same
	// id must not resurrect it into Funded — matches the frozen transition
	// table (Pending -> {Funded | Rejected | Cancelled}, no edges out of
	// Rejected).
	events := []*models.ChainEvent{
		ev("RedemptionRequested", "6", 10, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1000)}),
		ev("RedemptionRejected", "6", 11, 0, map[string]any{"reasonCode": "0xdead", "caller": "0xCCC"}),
		ev("RedemptionFunded", "6", 12, 0, map[string]any{"funder": "0xBBB", "quoteAmount": "950"}),
	}
	states := BuildStates(events, 1209600)
	if states["6"].Status != models.RedemptionRejected {
		t.Errorf("Status = %s, want Rejected (Funded after Rejected must be ignored)", states["6"].Status)
	}
}

// delayedRedemptionRequestRepo wraps a repository.RedemptionRequestRepository
// and inserts a small sleep around every write Reconcile's generation-swap
// performs — same rationale as sales.delayedPurchaseRepo:
// widens the window during which a concurrent reader's List() call can
// interleave with an in-flight rebuild, so the regression test below has a
// real chance of catching a reintroduced DeleteAll-then-reinsert bug.
type delayedRedemptionRequestRepo struct {
	repository.RedemptionRequestRepository
}

func (d delayedRedemptionRequestRepo) Upsert(ctx context.Context, r *models.RedemptionRequest) error {
	time.Sleep(2 * time.Millisecond)
	return d.RedemptionRequestRepository.Upsert(ctx, r)
}

func (d delayedRedemptionRequestRepo) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	time.Sleep(2 * time.Millisecond)
	return d.RedemptionRequestRepository.DeleteStaleGeneration(ctx, gen)
}

// TestReconcileNeverExposesEmptyOrPartialRedemptionsToConcurrentReaders
// mirrors sales' regression test — see that test's doc
// comment for the full scenario. Here the worst case is a reorg that
// replaces every seeded redemption request id with entirely different
// ones.
func TestReconcileNeverExposesEmptyOrPartialRedemptionsToConcurrentReaders(t *testing.T) {
	escrowAddr := common.HexToAddress("0x0000000000000000000000000000000000000B")
	events := memory.NewChainEventRepository()
	repo := delayedRedemptionRequestRepo{memory.NewRedemptionRequestRepository()}
	svc := New(nil, escrowAddr, repo, common.Address{})

	ctx := context.Background()
	seedRequested := func(id string, block uint64) {
		if err := events.Create(ctx, &models.ChainEvent{
			ChainID: 31337, Address: escrowAddr.Hex(), Name: "RedemptionRequested", TxHash: "req-" + id, LogIndex: 0, BlockNumber: block,
			Data: map[string]any{"id": id, "beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1000)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{"1", "2", "3"} {
		seedRequested(id, uint64(10+i))
	}
	if err := svc.Reconcile(ctx, events, 31337, 1209600); err != nil {
		t.Fatalf("seed Reconcile: %v", err)
	}
	seeded, err := repo.List(ctx, "")
	if err != nil || len(seeded) != 3 {
		t.Fatalf("seed: got %d redemption requests, err=%v", len(seeded), err)
	}

	// Reorg: every seeded id is gone, replaced by three entirely
	// different ones.
	if _, err := events.DeleteFromBlock(ctx, 31337, escrowAddr.Hex(), 0); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"4", "5", "6"} {
		seedRequested(id, uint64(30+i))
	}

	var minObserved int32 = 1 << 30
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			list, err := repo.List(ctx, "")
			if err != nil {
				t.Errorf("concurrent List: %v", err)
				return
			}
			if len(list) == 0 {
				t.Error("concurrent reader observed an EMPTY redemption read model mid-rebuild")
				return
			}
			for {
				cur := atomic.LoadInt32(&minObserved)
				if int32(len(list)) >= cur || atomic.CompareAndSwapInt32(&minObserved, cur, int32(len(list))) {
					break
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()

	if err := svc.Reconcile(ctx, events, 31337, 1209600); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	close(stop)
	wg.Wait()

	if minObserved < 3 {
		t.Errorf("concurrent reader observed as few as %d redemption requests mid-rebuild, want always >= 3", minObserved)
	}

	final, err := repo.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != 3 {
		t.Fatalf("final redemption request count = %d, want 3", len(final))
	}
	for _, r := range final {
		if r.ID == "1" || r.ID == "2" || r.ID == "3" {
			t.Errorf("stale pre-reorg redemption request %s survived the rebuild", r.ID)
		}
	}
}

func TestBuildStatesMultipleIndependentRequests(t *testing.T) {
	events := []*models.ChainEvent{
		ev("RedemptionRequested", "1", 10, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "100", "quoteAmount": "95", "createdAt": uint64(1000)}),
		ev("RedemptionRequested", "2", 11, 0, map[string]any{"beneficiary": "0xBBB", "rwaAmount": "200", "quoteAmount": "190", "createdAt": uint64(1001)}),
		ev("RedemptionFunded", "1", 12, 0, map[string]any{"funder": "0xCCC", "quoteAmount": "95"}),
	}
	states := BuildStates(events, 1209600)
	if len(states) != 2 {
		t.Fatalf("expected 2 states, got %d", len(states))
	}
	if states["1"].Status != models.RedemptionFunded {
		t.Errorf("id1 status = %s, want Funded", states["1"].Status)
	}
	if states["2"].Status != models.RedemptionPending {
		t.Errorf("id2 status = %s, want Pending", states["2"].Status)
	}
}
