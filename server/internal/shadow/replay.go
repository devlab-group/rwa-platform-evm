// Package shadow implements the shadow-replay and soak-test harness. It runs
// the production indexer against a scripted replay of chain state in an
// isolated in-memory database. The package has no signing key and cannot submit
// a transaction, because it never constructs a blockchain.TxManager,
// keys.Provider or anything else that can sign. See
// docs/operator/soak-runbook.md for running it for a release-candidate soak.
package shadow

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/indexer"
)

// LogSpec is one scripted log entry, a JSON-friendly form of types.Log
type LogSpec struct {
	Address     string   `json:"address"`
	BlockNumber uint64   `json:"blockNumber"`
	TxHash      string   `json:"txHash"`
	LogIndex    uint     `json:"logIndex"`
	Topics      []string `json:"topics"`
	Data        string   `json:"data,omitempty"`
	// Decodable, when explicitly false, makes the Runner's decoder reject this
	// log, which sends it through the indexer's dead-letter path
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

// HeaderSpec is one scripted block header. A reorg is two steps that set the
// same Number with different Seeds: indexer.FakeSource derives the header hash
// from Seed, so the canonical block at that height changes hash
type HeaderSpec struct {
	Number uint64 `json:"number"`
	Seed   byte   `json:"seed"`
}

// FaultKind names a scripted RPC fault to inject before a step's Poll call.
type FaultKind string

const (
	FaultNone          FaultKind = ""
	FaultRangeTooLarge FaultKind = "range_too_large"
	FaultTransient     FaultKind = "transient"
	// FaultMalformedNoise injects the same error as FaultTransient
	FaultMalformedNoise FaultKind = "malformed"
)

// Step is one scripted unit of replay: the chain-state changes to apply before
// the next Poll, plus whether that Poll is expected to fail
type Step struct {
	Description string       `json:"description"`
	Headers     []HeaderSpec `json:"headers,omitempty"`
	// RemoveLogsFrom, if set, drops every previously added log at or after this
	// block number before the step's Logs are added, modeling the old chain's
	// logs disappearing in a reorg
	RemoveLogsFrom *uint64   `json:"removeLogsFrom,omitempty"`
	Logs           []LogSpec `json:"logs,omitempty"`
	Head           uint64    `json:"head"`
	// Fault, if set, is returned as the step's first FilterLogs response (see
	// indexer.FakeSource.FilterLogsErrors). The indexer's bounded retry is
	// expected to recover within the same Poll call.
	Fault FaultKind `json:"fault,omitempty"`
	// ExpectPollError marks this step's Poll as expected to fail, for example on
	// a reorg deeper than the automatic recovery policy. The Runner then treats
	// a failure as a pass and a success as an error.
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

// LoadScenario reads a Scenario from a JSON file. The package has no exporter
// that records testnet history into this format.
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

// addressList converts Scenario.Addresses to common.Address values
func (sc *Scenario) addressList() []common.Address {
	out := make([]common.Address, len(sc.Addresses))
	for i, a := range sc.Addresses {
		out[i] = common.HexToAddress(a)
	}
	return out
}

// applyStep applies step to source before the caller's next Poll
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
