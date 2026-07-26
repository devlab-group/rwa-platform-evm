package shadow

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
)

// StepResult records one scripted Step's outcome.
type StepResult struct {
	Index       int           `json:"index"`
	Description string        `json:"description"`
	Duration    time.Duration `json:"duration"`
	// Err is non-empty if this step's Poll call failed unexpectedly (or
	// succeeded when ExpectPollError said it shouldn't have).
	Err string `json:"err,omitempty"`
}

// Report is a shadow replay run's full result. The release criteria —
// resource usage staying within defined thresholds, and final indexed
// state matching an independent reconciliation process — are meant to be
// checked against this, whether by a human reading it or an automated gate
// in a soak-test pipeline.
type Report struct {
	Steps             []StepResult       `json:"steps"`
	ResourceSnapshots []ResourceSnapshot `json:"resourceSnapshots"`
	// FinalEventCount is how many chain_events the indexer actually
	// persisted by the end of the run.
	FinalEventCount int `json:"finalEventCount"`
	// ExpectedEventCount/ExpectedDLQEntries are independently derived
	// directly from the scenario's final chain state (see Runner.Run),
	// NOT from what the indexer itself reported — an independent
	// reconciliation, not a tautology.
	ExpectedEventCount int `json:"expectedEventCount"`
	DLQEntries         int `json:"dlqEntries"`
	ExpectedDLQEntries int `json:"expectedDLQEntries"`
	// Errors collects every unexpected step failure/mismatch; empty AND
	// Reconciled true is the release-gate pass condition.
	Errors     []string `json:"errors,omitempty"`
	Reconciled bool     `json:"reconciled"`
}

// Runner drives a Scenario against the real indexer/reconciliation logic
// (indexer.Indexer, wired exactly as cmd/platform/main.go wires it —
// WithBlockHashRepository + WithDeadLetterQueue) over an isolated in-memory
// repository set, with no blockchain.TxManager, keys.Provider, or any other
// signing capability ever constructed: it holds no production signing keys
// and cannot submit a state-changing production transaction — a structural
// guarantee, not a configuration-based one.
type Runner struct {
	scenario *Scenario
}

// NewRunner constructs a Runner for scenario.
func NewRunner(scenario *Scenario) *Runner {
	return &Runner{scenario: scenario}
}

// Run executes every scripted Step in order, sampling resource usage after
// each one, then performs the final independent reconciliation. It returns
// a Report even on a scenario-level mismatch (Reconciled=false) — only a ctx
// cancellation or a genuine harness-construction error returns a non-nil
// error alongside it.
func (r *Runner) Run(ctx context.Context) (*Report, error) {
	sc := r.scenario
	repos := memory.New()
	source := indexer.NewFakeSource()

	undecodable := map[string]bool{} // "txHash:logIndex" -> scripted as undecodable
	for _, step := range sc.Steps {
		for _, l := range step.Logs {
			if !l.decodable() {
				undecodable[logKey(common.HexToHash(l.TxHash), l.LogIndex)] = true
			}
		}
	}
	decode := func(log types.Log) (string, map[string]any, error) {
		if undecodable[logKey(log.TxHash, log.Index)] {
			return "", nil, fmt.Errorf("shadow: scripted undecodable log %s", logKey(log.TxHash, log.Index))
		}
		name := "unknown"
		if len(log.Topics) > 0 {
			name = log.Topics[0].Hex()
		}
		return name, map[string]any{}, nil
	}

	opts := []indexer.Option{
		indexer.WithBlockHashRepository(repos.IndexerBlockHashes),
		indexer.WithDeadLetterQueue(repos.IndexerDeadLetters),
	}
	if sc.MaxLogRange > 0 {
		opts = append(opts, indexer.WithMaxLogRange(sc.MaxLogRange))
	}
	idx := indexer.New(source, repos.IndexerCheckpoints, repos.ChainEvents, sc.ChainID, sc.addressList(), sc.StartBlock, decode, opts...)

	report := &Report{}
	for i, step := range sc.Steps {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		applyStep(source, step)
		start := time.Now()
		pollErr := idx.Poll(ctx)
		sr := StepResult{Index: i, Description: step.Description, Duration: time.Since(start)}
		switch {
		case pollErr != nil && !step.ExpectPollError:
			sr.Err = pollErr.Error()
			report.Errors = append(report.Errors, fmt.Sprintf("step %d (%s): unexpected Poll error: %v", i, step.Description, pollErr))
		case pollErr == nil && step.ExpectPollError:
			sr.Err = "expected Poll to fail but it succeeded"
			report.Errors = append(report.Errors, fmt.Sprintf("step %d (%s): %s", i, step.Description, sr.Err))
		}
		report.Steps = append(report.Steps, sr)
		report.ResourceSnapshots = append(report.ResourceSnapshots, Sample())
	}

	if err := r.reconcile(ctx, sc, source, repos, undecodable, report); err != nil {
		return report, err
	}
	report.Reconciled = len(report.Errors) == 0 &&
		report.FinalEventCount == report.ExpectedEventCount &&
		report.DLQEntries == report.ExpectedDLQEntries
	return report, nil
}

// reconcile independently derives the expected canonical event/DLQ counts
// directly from source's final chain state (not from anything the indexer
// itself reported) and compares them against what actually got persisted,
// confirming the final indexed state matches an independent reconciliation.
func (r *Runner) reconcile(ctx context.Context, sc *Scenario, source *indexer.FakeSource, repos *repository.Repositories, undecodable map[string]bool, report *Report) error {
	configured := map[common.Address]bool{}
	for _, a := range sc.addressList() {
		configured[a] = true
	}

	canonical := map[string]bool{} // logKey -> decodable
	namesByAddress := map[common.Address]map[string]bool{}
	for _, l := range source.Logs() {
		if l.Removed || !configured[l.Address] {
			continue
		}
		key := logKey(l.TxHash, l.Index)
		decodable := !undecodable[key]
		canonical[key] = decodable
		if decodable {
			name := "unknown"
			if len(l.Topics) > 0 {
				name = l.Topics[0].Hex()
			}
			if namesByAddress[l.Address] == nil {
				namesByAddress[l.Address] = map[string]bool{}
			}
			namesByAddress[l.Address][name] = true
		}
	}
	for _, decodable := range canonical {
		if decodable {
			report.ExpectedEventCount++
		} else {
			report.ExpectedDLQEntries++
		}
	}

	for addr, names := range namesByAddress {
		for name := range names {
			evs, err := repos.ChainEvents.ListByName(ctx, sc.ChainID, addr.Hex(), name)
			if err != nil {
				return fmt.Errorf("shadow: reconcile ListByName: %w", err)
			}
			report.FinalEventCount += len(evs)
		}
	}

	entries, err := repos.IndexerDeadLetters.List(ctx)
	if err != nil {
		return fmt.Errorf("shadow: reconcile DLQ list: %w", err)
	}
	report.DLQEntries = len(entries)
	return nil
}

func logKey(txHash common.Hash, logIndex uint) string {
	return fmt.Sprintf("%s:%d", txHash.Hex(), logIndex)
}
