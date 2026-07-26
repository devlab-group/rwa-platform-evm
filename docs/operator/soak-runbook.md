# Shadow replay & soak-test runbook

Load tests and short RPC-failure simulations (`testnet-qualification.md` step 5) aren't enough to
validate a continuously running indexer and transaction-processing backend. This is the extra
release-candidate gate before a production deployment: replay a representative event history and
let the process run long enough to catch memory leaks, goroutine growth, and state drift.

This is an **operator-run procedure**. The repository ships the harness
(`server/internal/shadow`, driven by `server/ops/shadow`) and proves it works end-to-end in CI
(`server/internal/shadow/runner_test.go`); it does **not** itself run a 72-hour soak — that is a
release-time step a human schedules and evaluates, documented here.

## 1. What the harness guarantees structurally

`server/ops/shadow` only imports `server/internal/shadow` and (transitively) `server/internal/indexer`.
It never constructs a `blockchain.TxManager`, a `keys.Provider`, or anything else capable of
signing — "holds no production signing keys" and "cannot submit state-changing production
transactions" are true because the binary contains no code path that could, not because a flag is
off. It runs the **same** event-ingestion and reorg/rollback logic (`indexer.Indexer`, wired with
`WithBlockHashRepository` + `WithDeadLetterQueue`, identically to `cmd/platform/main.go`) against
an isolated `repository/memory` database — never the production Mongo.

## 2. Building the replay dataset

A scenario is a JSON file (`shadow.Scenario` — see `server/ops/shadow/testdata/scenario.json` for
a worked example covering every required input category below at small scale) listing scripted
`Step`s: header/log mutations to apply before each `Poll` cycle. For a real release-candidate soak,
export a genuine recorded testnet history (not the small illustrative fixture) covering:

- deployment events, mint attestations, burn attestations,
- whitelist additions and removals,
- transfers, direct purchases,
- redemption requests, funding, claims, cancellations,
- at least one simulated reorg (`removeLogsFrom` + a re-forked header — see the fixture's last two
  steps),
- at least one duplicate log redelivery (idempotent-replay coverage — the fixture's second step),
- at least one malformed/undecodable event (DLQ coverage — `"decodable": false`),
- at least one transient/oversized-response RPC fault (`"fault": "transient"` /
  `"fault": "range_too_large"`).

There is no exporter for a live testnet history in this repository yet; building one (or hand-
authoring a larger adversarial scenario) is part of preparing a specific release's soak run.

## 3. Running a soak

```
go run ./ops/shadow \
  -scenario path/to/release-candidate-scenario.json \
  -idle 72h \
  -sample 5m \
  -report soak-report.json
```

`-idle` keeps the process alive and resource-sampling after the scripted steps finish — point a
much longer/denser scenario at `-scenario` for genuine multi-day chain activity coverage; `-idle`
alone only proves the process is stable while otherwise mostly quiescent, which is a real but
partial signal, not a substitute for a busy scenario.

**Repeated disconnect/recovery cycles**: between soak segments, kill
and restart the `go run ./ops/shadow` process against the same scenario file with `-report`
pointed at the same path, and confirm in the resulting log/report that scanning resumed from the
last checkpoint state rather than re-processing or losing events — this exercises the same
restart-recovery path as `cmd/platform`'s indexer/tx-manager startup reconciliation
(see `operator-guide.md` §6).

Run the **actual production `cmd/platform` binary** against a real testnet in parallel per
`testnet-qualification.md` for the transaction-manager/signing-path coverage the shadow harness
deliberately excludes (it holds no keys) — the two runbooks are complementary, not substitutes for
each other.

## 4. Minimum duration

72 hours for a release-candidate gate; longer (a week+) is preferred before a production
deployment of a NEW release line. A patch release fixing an isolated, well-understood defect may
use operator judgement for a shorter soak, but must still pass this runbook's disconnect/recovery
requirement at least once.

## 5. What to monitor

From the harness's own `Report` (`finalEventCount`/`expectedEventCount`/`dlqEntries`/`reconciled`,
`resourceSnapshots`: heap, GC count, goroutine count, best-effort open-FD count — see
`server/internal/shadow/monitor.go`), **plus** the production server's own Prometheus `/metrics`
when running `cmd/platform` in parallel per §3 above:

- DB connections, RPC connection count, request/queue depth — standard Go/Mongo-driver/HTTP
  metrics already exposed.
- Event-processing lag — indexer checkpoint block vs. chain head.
- DLQ growth — `indexer_dead_letters` collection size over time (should track known-bad injected
  events, not grow unboundedly on its own).
- Redemption/reconciliation backlog — `RedemptionsPendingCount`,
  `RedemptionsFundedUnclaimedCount` (`internal/metrics`).
- CPU usage — OS-level (`top`/`pidstat`) alongside the process.
- Restart recovery time — wall-clock time from process start to the indexer/tx-manager's first
  successful reconciliation pass (logged at startup — see `operator-guide.md` §6's transaction-
  manager/indexer recovery bullets).

Compare metrics across **multiple, time-separated windows** of the run (e.g. hour 1 vs. hour 36
vs. hour 71), not just start-vs-end — `shadow.GrowthCheck` is provided as a simple first/last
comparison helper; a genuine soak evaluation should apply it (or eyeball a plotted series) across
several windows to distinguish "grew once during warmup then stabilized" from "still climbing."

## 6. Failure criteria (fail the release-candidate gate)

- Continuously increasing memory usage without stabilization across the monitored windows.
- Unbounded goroutine growth.
- Unbounded queue or DLQ growth (i.e. growth NOT explained by intentionally-scripted bad events).
- Lost or duplicated canonical events — `report.reconciled == false`, or a live-run event count
  that diverges from an independent chain-side count.
- Database state diverging from an independently reconstructed chain state (see §7).
- Inability to resume from the last verified checkpoint after a disconnect/recovery cycle (§3).
- Incorrect redemption status after a reorg (exercise via `redemption/redemption_test.go`-style
  scenarios folded into the scenario file, or via the live-run parallel per §3).
- Unavailable health checks (`/healthz`-equivalent) while the process is otherwise running.

## 7. Independent final-state reconciliation

The harness's own `Runner.reconcile` step (`server/internal/shadow/runner.go`) already performs
one independent reconciliation automatically: it derives the expected canonical event/DLQ counts
directly from the scripted chain state (never from what the indexer itself reported) and compares
them to what was actually persisted — `report.reconciled` is that check's pass/fail bit.

For a live parallel run against a real testnet (§3), perform a SECOND, separately-implemented
reconciliation before sign-off: query the chain directly (e.g. `cast logs` / a block explorer API)
for the canonical event count and compare it to `chain_events` collection counts, and independently
recompute at least one derived read model (e.g. total minted supply from `SupplyIncreased` events)
against the token contract's own `totalSupply()`.

## 8. Sign-off

Release-candidate hardening is complete only when:

- A representative testnet event history (§2) has been replayed and `report.reconciled == true`.
- The soak ran for the required duration (§4) with resource usage within the failure criteria (§6).
- At least one disconnect/recovery cycle (§3) succeeded.
- The independent final-state reconciliation (§7) matches.
- Any discovered leak or divergence defect is resolved (with a follow-up soak proving the fix) or
  explicitly accepted in the release notes before shipping.

Record the scenario file used, the `-report` output, the duration actually run, and the operator
who signed off in the release notes — same convention as `testnet-qualification.md`'s sign-off.
