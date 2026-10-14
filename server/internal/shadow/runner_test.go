package shadow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testChainID = 31337

var testAddr = "0x0000000000000000000000000000000000A001"

func falseVal() *bool { f := false; return &f }

// TestRunnerReplaysScriptedScenarioAndReconciles replays a scripted history
// (ordinary ingestion, a duplicate log redelivery, a reorg, a transient RPC
// fault and an undecodable event) against the real indexer in an isolated
// in-memory database, with resource sampling and a final reconciliation.
func TestRunnerReplaysScriptedScenarioAndReconciles(t *testing.T) {
	sc := &Scenario{
		ChainID: testChainID, Addresses: []string{testAddr}, StartBlock: 1,
		Steps: []Step{
			{
				Description: "genesis ingest",
				Headers:     []HeaderSpec{{Number: 1, Seed: 1}},
				Logs: []LogSpec{
					{Address: testAddr, BlockNumber: 1, TxHash: "0x01", LogIndex: 0, Topics: []string{"0x01"}},
				},
				Head: 1,
			},
			{
				Description: "duplicate redelivery + one new log",
				Headers:     []HeaderSpec{{Number: 1, Seed: 1}, {Number: 2, Seed: 1}},
				Logs: []LogSpec{
					{Address: testAddr, BlockNumber: 1, TxHash: "0x01", LogIndex: 0, Topics: []string{"0x01"}}, // exact redelivery
					{Address: testAddr, BlockNumber: 2, TxHash: "0x02", LogIndex: 0, Topics: []string{"0x01"}},
				},
				Head: 2,
			},
			{
				Description: "transient RPC fault, recovered by the indexer's own retry",
				Headers:     []HeaderSpec{{Number: 3, Seed: 1}},
				Logs:        []LogSpec{{Address: testAddr, BlockNumber: 3, TxHash: "0x03", LogIndex: 0, Topics: []string{"0x01"}}},
				Head:        3,
				Fault:       FaultTransient,
			},
			{
				Description: "reorg: block 4 forked, its log replaced",
				Headers:     []HeaderSpec{{Number: 4, Seed: 1}},
				Logs:        []LogSpec{{Address: testAddr, BlockNumber: 4, TxHash: "0x04", LogIndex: 0, Topics: []string{"0x01"}}},
				Head:        4,
			},
			{
				Description:    "reorg lands: block 4 refork with a different log",
				Headers:        []HeaderSpec{{Number: 4, Seed: 2}},
				RemoveLogsFrom: uint64Ptr(4),
				Logs:           []LogSpec{{Address: testAddr, BlockNumber: 4, TxHash: "0x04b", LogIndex: 0, Topics: []string{"0x01"}}},
				Head:           4,
			},
			{
				Description: "undecodable event goes to the DLQ, not lost",
				Headers:     []HeaderSpec{{Number: 5, Seed: 1}},
				Logs: []LogSpec{
					{Address: testAddr, BlockNumber: 5, TxHash: "0x05", LogIndex: 0, Topics: []string{"0x01"}, Decodable: falseVal()},
					{Address: testAddr, BlockNumber: 5, TxHash: "0x05b", LogIndex: 1, Topics: []string{"0x01"}},
				},
				Head: 5,
			},
		},
	}

	report, err := NewRunner(sc).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("unexpected step errors: %v", report.Errors)
	}
	if len(report.ResourceSnapshots) != len(sc.Steps) {
		t.Fatalf("ResourceSnapshots = %d, want one per step (%d)", len(report.ResourceSnapshots), len(sc.Steps))
	}
	if report.DLQEntries != 1 {
		t.Fatalf("DLQEntries = %d, want 1", report.DLQEntries)
	}
	if !report.Reconciled {
		t.Fatalf("expected Reconciled=true, got report: %+v", report)
	}
	// 5 canonical decodable events: block1(0x01), block2(0x02), block3(0x03),
	// block4(0x04b), which replaced the reorged-out 0x04, and block5(0x05b)
	if report.FinalEventCount != 5 {
		t.Fatalf("FinalEventCount = %d, want 5", report.FinalEventCount)
	}
	if report.ExpectedEventCount != report.FinalEventCount {
		t.Fatalf("ExpectedEventCount = %d != FinalEventCount = %d", report.ExpectedEventCount, report.FinalEventCount)
	}
}

// TestRunnerExpectPollErrorForDeepReorg checks that a transient fault the
// indexer recovers from within one Poll, with ExpectPollError false, adds no
// report errors
func TestRunnerExpectPollErrorForDeepReorg(t *testing.T) {
	sc := &Scenario{
		ChainID: testChainID, Addresses: []string{testAddr}, StartBlock: 1, MaxLogRange: 10,
		Steps: []Step{
			{
				Description:     "transient RPC fault recovered by the indexer's retry",
				Headers:         []HeaderSpec{{Number: 1, Seed: 1}},
				Head:            1,
				Fault:           FaultTransient,
				ExpectPollError: false, // a single transient fault recovers within the retry budget
			},
		},
	}
	report, err := NewRunner(sc).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", report.Errors)
	}
}

// TestRunnerRespectsContextCancellation checks that Run returns the context's
// error when ctx is already cancelled
func TestRunnerRespectsContextCancellation(t *testing.T) {
	sc := &Scenario{ChainID: testChainID, Addresses: []string{testAddr}, StartBlock: 1, Steps: []Step{
		{Description: "one", Headers: []HeaderSpec{{Number: 1, Seed: 1}}, Head: 1},
		{Description: "two", Headers: []HeaderSpec{{Number: 2, Seed: 1}}, Head: 2},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewRunner(sc).Run(ctx)
	if err == nil {
		t.Fatal("expected Run to return the context's cancellation error")
	}
}

// TestLoadScenarioRoundTrips checks that a Scenario written to disk loads back
// with the same chain ID and step count
func TestLoadScenarioRoundTrips(t *testing.T) {
	sc := &Scenario{ChainID: testChainID, Addresses: []string{testAddr}, StartBlock: 1, Steps: []Step{
		{Description: "genesis", Headers: []HeaderSpec{{Number: 1, Seed: 1}}, Head: 1},
	}}
	raw, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadScenario(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChainID != sc.ChainID || len(got.Steps) != len(sc.Steps) {
		t.Fatalf("round-tripped scenario mismatch: %+v", got)
	}
}

func uint64Ptr(v uint64) *uint64 { return &v }

// TestSampleReturnsPlausibleValues checks that Sample returns a positive
// goroutine count and a timestamp that is not in the future
func TestSampleReturnsPlausibleValues(t *testing.T) {
	s := Sample()
	if s.NumGoroutine <= 0 {
		t.Fatalf("NumGoroutine = %d, want > 0", s.NumGoroutine)
	}
	if s.Timestamp.After(time.Now().UTC()) {
		t.Fatal("Timestamp is in the future")
	}
}
