// Command shadow runs the shadow-replay / soak-test harness
// (server/internal/shadow) against a scripted replay dataset. It never
// dials a live chain, never opens a
// real database connection, and never constructs anything capable of
// signing a transaction — it only imports server/internal/shadow and
// server/internal/indexer, neither of which pulls in
// server/internal/blockchain's TxManager/Signer or server/internal/keys at
// all, so "cannot submit state-changing production transactions" is
// structural, not a runtime check that could be misconfigured away.
//
// Usage:
//
//	go run ./ops/shadow -scenario testdata/scenario.json
//	go run ./ops/shadow -scenario testdata/scenario.json -idle 72h -sample 5m
//
// See docs/operator/soak-runbook.md for the release-candidate soak
// procedure this drives and the failure criteria to apply to its output.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rwa-platform/server/internal/shadow"
)

func main() {
	scenarioPath := flag.String("scenario", "", "path to a shadow.Scenario JSON file (required)")
	idle := flag.Duration("idle", 0, "after the scripted steps complete, keep polling with no new data for this long — for a real extended-duration soak run (e.g. -idle 72h) rather than just proving the harness works")
	sampleEvery := flag.Duration("sample", time.Minute, "resource-sample interval during the idle phase")
	reportPath := flag.String("report", "", "write the final JSON Report here (default: stdout only)")
	flag.Parse()

	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "shadow: -scenario is required")
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, *scenarioPath, *idle, *sampleEvery, *reportPath); err != nil {
		log.Fatalf("shadow: %v", err)
	}
}

func run(ctx context.Context, scenarioPath string, idle, sampleEvery time.Duration, reportPath string) error {
	sc, err := shadow.LoadScenario(scenarioPath)
	if err != nil {
		return err
	}
	log.Printf("shadow: loaded scenario %s: chainId=%d addresses=%v steps=%d", scenarioPath, sc.ChainID, sc.Addresses, len(sc.Steps))

	report, err := shadow.NewRunner(sc).Run(ctx)
	if err != nil {
		return fmt.Errorf("scripted replay: %w", err)
	}
	log.Printf("shadow: scripted replay complete: %d steps, %d events, %d DLQ entries, reconciled=%v",
		len(report.Steps), report.FinalEventCount, report.DLQEntries, report.Reconciled)

	if idle > 0 {
		idleUntil(ctx, idle, sampleEvery, report)
	}

	return writeReport(report, reportPath)
}

// idleUntil keeps sampling resource usage (with no new scripted chain
// state — a real extended run should instead point -scenario at a much
// longer recorded history) for idle, appending to report.ResourceSnapshots,
// so an operator running an actual 72h+ soak per docs/operator/soak-runbook.md
// gets a full resource-usage timeline to evaluate the failure criteria
// against, not just the scripted portion's snapshots.
func idleUntil(ctx context.Context, idle, sampleEvery time.Duration, report *shadow.Report) {
	deadline := time.Now().Add(idle)
	ticker := time.NewTicker(sampleEvery)
	defer ticker.Stop()
	log.Printf("shadow: entering idle resource-monitoring phase for %s (sampling every %s)", idle, sampleEvery)
	for {
		select {
		case <-ctx.Done():
			log.Printf("shadow: idle phase interrupted: %v", ctx.Err())
			return
		case <-ticker.C:
			snap := shadow.Sample()
			report.ResourceSnapshots = append(report.ResourceSnapshots, snap)
			log.Printf("shadow: sample heap=%s goroutines=%d openFDs=%d", shadow.FormatBytes(snap.HeapAllocBytes), snap.NumGoroutine, snap.OpenFDs)
			if time.Now().After(deadline) {
				return
			}
		}
	}
}

func writeReport(report *shadow.Report, path string) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if path == "" {
		fmt.Println(string(raw))
		return nil
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write report %s: %w", path, err)
	}
	log.Printf("shadow: report written to %s", path)
	return nil
}
