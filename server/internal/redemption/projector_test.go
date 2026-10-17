package redemption

import (
	"context"
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

func TestBuildStatesSetsFundedAtFromIndexedAt(t *testing.T) {
	indexed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	funded := ev("RedemptionFunded", "1", 12, 0, map[string]any{"funder": "0xBBB"})
	funded.IndexedAt = indexed
	states := BuildStates([]*models.ChainEvent{
		ev("RedemptionRequested", "1", 10, 0, map[string]any{"createdAt": uint64(1000)}),
		funded,
	}, 1209600)
	if got := states["1"].FundedAt; !got.Equal(indexed) {
		t.Errorf("FundedAt = %v, want %v", got, indexed)
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

func TestBuildStatesIgnoresFundedWithoutRequested(t *testing.T) {
	// a Funded event for an id that was never requested must not create state
	events := []*models.ChainEvent{
		ev("RedemptionFunded", "4", 5, 0, map[string]any{"funder": "0xBBB", "quoteAmount": "1"}),
	}
	states := BuildStates(events, 1209600)
	if _, ok := states["4"]; ok {
		t.Errorf("expected no state for an id that was never requested")
	}
}

func TestBuildStatesAppliesEventsInBlockOrderRegardlessOfInputOrder(t *testing.T) {
	// Unsorted input must replay by (blockNumber, logIndex)
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
	// A duplicate Funded event must not move Rejected back to Funded
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

// probedRedemptionRequestRepo reads the read model between every generation write, like
// sales.probedPurchaseRepo
type probedRedemptionRequestRepo struct {
	repository.RedemptionRequestRepository
	probe func()
}

func (d probedRedemptionRequestRepo) Upsert(ctx context.Context, r *models.RedemptionRequest) error {
	d.probe()
	return d.RedemptionRequestRepository.Upsert(ctx, r)
}

func (d probedRedemptionRequestRepo) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	d.probe()
	return d.RedemptionRequestRepository.DeleteStaleGeneration(ctx, gen)
}

// Replace every seeded request ID during a reorg; concurrent reads must not fall below
// the seeded count, as in the sales test
func TestReconcileNeverExposesEmptyOrPartialRedemptionsBetweenWrites(t *testing.T) {
	escrowAddr := common.HexToAddress("0x0000000000000000000000000000000000000B")
	events := memory.NewChainEventRepository()
	inner := memory.NewRedemptionRequestRepository()
	armed, probes, minObserved := false, 0, 1<<30
	repo := probedRedemptionRequestRepo{RedemptionRequestRepository: inner, probe: func() {
		if !armed {
			return // only the rebuild under test is observed
		}
		probes++
		list, err := inner.List(context.Background(), "")
		if err != nil {
			t.Errorf("probe List: %v", err)
			return
		}
		minObserved = min(minObserved, len(list))
	}}
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

	// reorg: every seeded id is gone, replaced by three different ones
	if _, err := events.DeleteFromBlock(ctx, 31337, escrowAddr.Hex(), 0); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"4", "5", "6"} {
		seedRequested(id, uint64(30+i))
	}

	armed = true
	if err := svc.Reconcile(ctx, events, 31337, 1209600); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if probes == 0 {
		t.Fatal("the rebuild never wrote, so no intermediate state was observed")
	}
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

// Illegal transitions must preserve state (docs/spec/redemption-state-machine.md);
// each row replays Requested, a legal prefix, then an illegal event
func TestBuildStatesIgnoresTransitionsTheStateMachineForbids(t *testing.T) {
	req := func() *models.ChainEvent {
		return ev("RedemptionRequested", "9", 1, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1000)})
	}
	funded := func(b uint64) *models.ChainEvent {
		return ev("RedemptionFunded", "9", b, 0, map[string]any{"funder": "0xBBB"})
	}
	completed := func(b uint64) *models.ChainEvent { return ev("RedemptionCompleted", "9", b, 0, nil) }
	rejected := func(b uint64) *models.ChainEvent {
		return ev("RedemptionRejected", "9", b, 0, map[string]any{"reasonCode": "0xdead"})
	}
	cancelled := func(b uint64) *models.ChainEvent { return ev("RedemptionCancelled", "9", b, 0, nil) }

	tests := []struct {
		name   string
		events []*models.ChainEvent
		want   models.RedemptionStatus
	}{
		{"Pending cannot jump to Completed", []*models.ChainEvent{req(), completed(2)}, models.RedemptionPending},
		{"Funded cannot be Cancelled", []*models.ChainEvent{req(), funded(2), cancelled(3)}, models.RedemptionFunded},
		{"Funded cannot be Rejected", []*models.ChainEvent{req(), funded(2), rejected(3)}, models.RedemptionFunded},
		{"Funded cannot be funded twice", []*models.ChainEvent{req(), funded(2), funded(3)}, models.RedemptionFunded},
		{"Completed is terminal against a duplicate Completed", []*models.ChainEvent{req(), funded(2), completed(3), completed(4)}, models.RedemptionCompleted},
		{"Completed cannot be Cancelled", []*models.ChainEvent{req(), funded(2), completed(3), cancelled(4)}, models.RedemptionCompleted},
		{"Completed cannot be Rejected", []*models.ChainEvent{req(), funded(2), completed(3), rejected(4)}, models.RedemptionCompleted},
		{"Rejected cannot be Completed", []*models.ChainEvent{req(), rejected(2), completed(3)}, models.RedemptionRejected},
		{"Rejected cannot be Cancelled", []*models.ChainEvent{req(), rejected(2), cancelled(3)}, models.RedemptionRejected},
		{"Rejected cannot be rejected again", []*models.ChainEvent{req(), rejected(2), rejected(3)}, models.RedemptionRejected},
		{"Cancelled cannot be Funded", []*models.ChainEvent{req(), cancelled(2), funded(3)}, models.RedemptionCancelled},
		{"Cancelled cannot be Rejected", []*models.ChainEvent{req(), cancelled(2), rejected(3)}, models.RedemptionCancelled},
		{"Cancelled cannot be Completed", []*models.ChainEvent{req(), cancelled(2), completed(3)}, models.RedemptionCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			states := BuildStates(tt.events, 1209600)
			if got := states["9"].Status; got != tt.want {
				t.Errorf("Status = %s, want %s", got, tt.want)
			}
		})
	}
}

// An illegal event must not overwrite the data of the transition that already happened
func TestBuildStatesIllegalTransitionKeepsEarlierDetails(t *testing.T) {
	events := []*models.ChainEvent{
		ev("RedemptionRequested", "7", 1, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1000)}),
		ev("RedemptionRejected", "7", 2, 0, map[string]any{"reasonCode": "0xdead"}),
		ev("RedemptionRejected", "7", 3, 0, map[string]any{"reasonCode": "0xbeef"}),
	}
	r := BuildStates(events, 100)["7"]
	if r.ReasonCode != "0xdead" || r.Status != models.RedemptionRejected {
		t.Errorf("status/reason = %s/%s, want Rejected/0xdead", r.Status, r.ReasonCode)
	}
}
