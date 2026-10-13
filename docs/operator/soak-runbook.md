# Shadow replay and soak-test runbook

Before deploying a release candidate to production, replay a representative event history and
run a live testnet soak. Short load tests and the failure drills in
[testnet-qualification.md](testnet-qualification.md) do not establish stability over days.
Watch for memory leaks, goroutine growth and state drift.

The gate requires both runs:

- Shadow replay: `server/ops/shadow`, built on `server/internal/shadow`, exercises the indexer
  against a scripted chain. CI covers the replay through `server/internal/shadow/runner_test.go`.
- Live soak: the production `cmd/platform` binary runs against a real testnet configured through
  the qualification runbook. It exercises platform event decoding, read models, transaction
  management, signing and restart recovery.

The repository provides the harness and tests, but does not automate a 72-hour soak. The
release operator schedules the runs and evaluates their evidence.

## 1. What the harness covers

`shadow.Runner` constructs the production `indexer.Indexer` with an in-memory repository and
`indexer.FakeSource`. It constructs no transaction manager, key provider or live RPC client,
and neither loads signing keys nor submits live transactions.

The replay uses `WithBlockHashRepository` and `WithDeadLetterQueue`, as `cmd/platform` does.
Its limits are:

- State lives in `internal/dal/memory` and is lost on restart. The harness also omits production's
  atomic MongoDB chunk commit (`WithChunkCommitter`).
- The placeholder decoder uses the first topic as the event name when present and decodes no fields. It builds
  no purchase, redemption or supply read models.
- The scripted source replaces the RPC node, including simulated RPC errors.

Use replay to test ingestion, duplicate redelivery, reorg rollback, dead-letter handling,
fault recovery and resource use during those steps. Use the live run for read-model correctness,
restart recovery and sustained activity (§3).

## 2. Building the replay dataset

A scenario is a JSON `shadow.Scenario` containing `chainId`, contract `addresses`, `startBlock`,
optional `maxLogRange` and `steps`. Each step applies headers and logs, sets the scripted head
and runs one indexer poll.

`server/ops/shadow/testdata/scenario.json` has six steps: duplicate redelivery in step 2, a
transient RPC fault in step 3, a reorg in steps 4 and 5, and an undecodable log in step 6. Its
logs are placeholders rather than platform events.

A release-candidate dataset must include:

- a reorg using `removeLogsFrom` and a replacement header, as in example steps 4 and 5;
- duplicate log redelivery;
- an undecodable log with `"decodable": false`;
- a transient RPC fault with `"fault": "transient"`;
- an oversized-response fault with `"fault": "range_too_large"`.

For representative load, use recorded platform history: deployments, mints, burns, compliance
changes, transfers, purchases, and redemption requests, funding, claims and cancellations.
The placeholder decoder cannot validate their fields, but event volume and distribution across
blocks and contracts still load the indexer. The repository has no live-history exporter;
prepare one or write a larger adversarial scenario for the release.

## 3. Running a soak

From `server/`:

```
go run ./ops/shadow \
  -scenario path/to/release-candidate-scenario.json \
  -idle 72h \
  -sample 5m \
  -report soak-report.json
```

The CLI replays the steps and reconciles their results first. During `-idle`, it samples
resources; it does not poll the indexer or apply further chain activity. A larger scenario
adds replay work, but does not schedule that work over the idle period. The live run provides
the sustained indexer and transaction-processing coverage.

`-sample` sets the idle-phase sampling interval, defaulting to one minute. The runner also
samples after each replay step. `-report` writes the final JSON report; without it, the report
goes to stdout. Review `report.reconciled` and `errors`: a successful CLI exit alone does not
prove that replay passed.

### Live run

In parallel, run `cmd/platform` against a testnet configured through
[testnet-qualification.md](testnet-qualification.md). Both replay and live evidence are required.

### Restart cycles

Between live soak segments, stop and restart `cmd/platform` while preserving its database.
Record the checkpoint before stopping, then check `lastIndexedBlock` from `GET /healthz` after
restart. It should resume from the saved checkpoint, allowing for subsequent advancement or
a verified reorg rewind, rather than starting over at `contract.start_block`.

Confirm that `/readyz` returns 200 and, separately, that indexing catches up to the chain head.
Readiness alone does not establish catch-up. Check the event reconciliation in §7 for losses or
duplicates. This exercises indexer and transaction-manager startup recovery; see the
[operator guide, §6](operator-guide.md#6-backup-and-recovery).

Do restart tests on the live process. Restarting the in-memory harness starts a new replay.

## 4. Minimum duration

Run the release-candidate soak for at least 72 hours. Prefer a week or more before the first
production deployment of a new release line. At the operator's judgment, a patch fixing an
isolated, well-understood defect may use a shorter soak, but must still pass at least one
restart cycle (§3). Record the elapsed duration, including any early interruption.

## 5. What to monitor

In the harness report, compare `finalEventCount` with `expectedEventCount` and `dlqEntries`
with `expectedDLQEntries`, and check `reconciled`. `resourceSnapshots` records heap bytes,
heap objects, GC count, goroutines and open files. The open-file count is best effort on Linux;
zero means unavailable on other systems or when `/proc` cannot be read. See
`server/internal/shadow/runner.go` and `monitor.go`.

For the live run, use `/metrics` and the health endpoints; see the
[operator guide, §5](operator-guide.md#5-monitoring-and-alerts):

| Signal                                 | Source                                                                                                                                                  |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Memory, goroutines, open files and CPU | `go_goroutines`, `go_memstats_heap_alloc_bytes`, `process_resident_memory_bytes`, `process_open_fds`, `process_cpu_seconds_total`; or `top` / `pidstat` |
| Request volume and latency             | `rwa_http_requests_total`, `rwa_http_request_duration_seconds`                                                                                          |
| Indexer lag                            | compare `lastIndexedBlock` from `GET /healthz` with the chain head; there is no dedicated lag metric                                                    |
| Dead-letter growth                     | size of `indexer_dead_letters` or `opsctl dlq list`; growth must correspond to injected bad events                                                      |
| Redemption backlog                     | `rwa_redemptions_pending_count`, `rwa_redemptions_funded_unclaimed_count`                                                                               |
| Background errors and storage health   | `rwa_read_model_reconcile_errors_total`, `rwa_storage_up`                                                                                               |
| Restart recovery time                  | elapsed time from process start until `/readyz` returns 200 and indexing reaches the chain head                                                         |

The server exports no dedicated MongoDB connection-pool, RPC-connection or queue-depth metrics.

Compare separated windows, such as hours 1, 36 and 71, to distinguish startup growth from a
continuing increase. `shadow.GrowthCheck` provides two-sample growth checks; apply it to several
pairs or plot the series instead of relying on the first and last samples.

## 6. Failure criteria (fail the release-candidate gate)

Fail the gate for:

- memory use that continues rising without stabilizing across the monitored windows;
- unbounded goroutine growth;
- dead-letter growth unexplained by injected bad events;
- lost or duplicated canonical events, `report.reconciled == false`, or a live event count that
  differs from an independent chain count;
- database state that differs from independently reconstructed chain state (§7);
- failure to recover the checkpoint after restart (§3);
- an incorrect live redemption status after a reorg; the harness has no redemption read model
  and cannot test this;
- `/healthz` failure or persistent `/readyz` failure while the process is otherwise running.

## 7. Independent final-state reconciliation

`Runner.reconcile` in `server/internal/shadow/runner.go` derives expected event and dead-letter
counts from the scripted source, independently of indexer results. It compares them with
persisted counts. `report.reconciled` is true only when those counts match and there are no
unexpected step errors.

Before live sign-off, reconcile again through a separate implementation. Query the chain
directly with `cast logs` or a block-explorer API, then compare canonical event counts with
`chain_events` for the same chain, contracts and block range. Account separately for logs
sent to the dead-letter queue.

Independently reconstruct at least one stored read-model value, such as a redemption's status
from its canonical events, and compare it with the database. A complementary supply check
sums `Minted` amounts minus `Burned` amounts from `SupplyController` and compares the result
with the token's `totalSupply()`.

## 8. Sign-off

Release-candidate hardening requires:

- replay of representative history (§2) with `report.reconciled == true`;
- the required soak duration (§4) without a failure criterion (§6);
- at least one successful restart cycle (§3);
- matching independent final-state reconciliation (§7).

For every leak or divergence found, record either a fix verified by a follow-up soak or
explicit acceptance in the release notes before shipping. Acceptance records do not establish
that a run passed the criteria above.

Include the scenario file, `-report` output, actual duration and signing operator in the release
notes, alongside the [testnet qualification sign-off](testnet-qualification.md#sign-off).
