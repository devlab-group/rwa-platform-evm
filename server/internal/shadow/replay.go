// Package shadow implements the shadow-replay / soak-test harness: drive
// the SAME indexer/reconciliation logic the production server runs against
// a scripted replay of chain
// state, in an isolated in-memory database, with no signing key and no
// ability to submit a state-changing production transaction — structurally
// guaranteed by never constructing a blockchain.TxManager, keys.Provider,
// or anything else capable of signing, not merely by configuration. See
// docs/operator/soak-runbook.md for how to run this against a real
// recorded testnet history for a release-candidate soak.
package shadow

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/indexer"
)

// LogSpec is one scripted log entry, the JSON-friendly shape of a
// types.Log.
type LogSpec struct {
	Address     string   `json:"address"`
	BlockNumber uint64   `json:"blockNumber"`
	TxHash      string   `json:"txHash"`
	LogIndex    uint     `json:"logIndex"`
	Topics      []string `json:"topics"`
	Data        string   `json:"data,omitempty"`
	// Decodable, when explicitly set false, makes this log fail EventDecoder
	// (see Runner's decoder) — a scripted "malformed RPC response" /
	// undecodable-event scenario exercising the indexer's DLQ path end to end,
	// rather than taking the indexer's own unit-tested decode-failure path on
	// faith here too.
	Decodable *bool `json:"decodable,omitempty"`
}

func (s LogSpec) toLog() types.Log {
	topics := make([]common.Hash, len(s.Topics))
	for i, t := range s.Topics {
		topics[i] = common.HexToHash(t)
	}
	return types.Log{
		Address: common.HexToAddress(s.Address), BlockNumber: s.BlockNumber,
		TxHash: common.HexToHash(s.TxHash), Index: s.LogIndex, Topics: topics,
		Data: common.FromHex(s.Data),
	}
}

func (s LogSpec) decodable() bool {
	return s.Decodable == nil || *s.Decodable
}

// HeaderSpec is one scripted block header. A reorg is expressed by two
// steps setting the SAME Number with a different Seed — indexer.FakeSource
// derives the header's hash from Seed, so this changes what the "canonical"
// block at that height hashes to, exactly like a real reorg would.
type HeaderSpec struct {
	Number uint64 `json:"number"`
	Seed   byte   `json:"seed"`
}

// FaultKind names a scripted RPC fault to inject before a step's Poll call.
type FaultKind string

const (
	FaultNone           FaultKind = ""
	FaultRangeTooLarge  FaultKind = "range_too_large"
	FaultTransient      FaultKind = "transient"
	FaultMalformedNoise FaultKind = "malformed"
)

// Step is one scripted unit of replay: chain-state mutations to apply
// before the next Poll cycle, plus what's expected to happen.
type Step struct {
	Description string       `json:"description"`
	Headers     []HeaderSpec `json:"headers,omitempty"`
	// RemoveLogsFrom, if set, drops every previously-added log at/after
	// this block number before adding this step's new Logs — models a
	// reorg's old-chain logs disappearing.
	RemoveLogsFrom *uint64   `json:"removeLogsFrom,omitempty"`
	Logs           []LogSpec `json:"logs,omitempty"`
	Head           uint64    `json:"head"`
	// Fault, if set, is injected as this step's FIRST FilterLogs response
	// (see indexer.FakeSource.FilterLogsErrors) — the indexer's own bounded
	// retry/backoff must recover from it within the same Poll call.
	Fault FaultKind `json:"fault,omitempty"`
	// ExpectPollError documents that THIS step's Poll call is scripted to
	// fail outright (e.g. a reorg deeper than the configured automatic
	// policy) — Runner does not treat that as a harness failure when set.
	ExpectPollError bool `json:"expectPollError,omitempty"`
}

// Scenario is a complete scripted replay dataset.
type Scenario struct {
	ChainID     int64    `json:"chainId"`
	Addresses   []string `json:"addresses"`
	StartBlock  uint64   `json:"startBlock"`
	MaxLogRange uint64   `json:"maxLogRange,omitempty"`
	Steps       []Step   `json:"steps"`
}

// LoadScenario reads a Scenario from a JSON file — the on-disk form of a
// replay of real testnet blocks, logs, and transactions. A real
// recorded-testnet-history exporter is an ops-side concern outside this
// package's scope.
func LoadScenario(path string) (*Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("shadow: read scenario %s: %w", path, err)
	}
	var sc Scenario
	if err := json.Unmarshal(raw, &sc); err != nil {
		return nil, fmt.Errorf("shadow: parse scenario %s: %w", path, err)
	}
	return &sc, nil
}

// addressList converts Scenario.Addresses to common.Address.
func (sc *Scenario) addressList() []common.Address {
	out := make([]common.Address, len(sc.Addresses))
	for i, a := range sc.Addresses {
		out[i] = common.HexToAddress(a)
	}
	return out
}

// applyStep mutates source per step, ahead of the caller's next idx.Poll.
func applyStep(source *indexer.FakeSource, step Step) {
	for _, h := range step.Headers {
		source.SetHeader(h.Number, h.Seed)
	}
	if step.RemoveLogsFrom != nil {
		source.RemoveLogsAtOrAfter(*step.RemoveLogsFrom)
	}
	for _, l := range step.Logs {
		source.AddLog(l.toLog())
	}
	if step.Head > 0 {
		source.SetHead(step.Head)
	}
	switch step.Fault {
	case FaultRangeTooLarge:
		source.FilterLogsErrors = append(source.FilterLogsErrors, indexer.ErrRangeTooLarge)
	case FaultTransient, FaultMalformedNoise:
		source.FilterLogsErrors = append(source.FilterLogsErrors, indexer.ErrTransient)
	}
}
