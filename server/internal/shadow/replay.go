// Package shadow runs the production indexer against scripted chain replay in isolated
// in-memory repositories. It constructs no blockchain.TxManager, keys.Provider or
// signer and cannot sign or submit transactions. See docs/operator/soak-runbook.md for
// release-candidate soak runs.
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
	// Decodable=false makes Runner reject the log into the indexer's dead-letter path
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

// HeaderSpec describes a block header; setting the same Number with different Seeds
// across steps changes FakeSource's derived hash, modeling a reorg
type HeaderSpec struct {
	Number uint64 `json:"number"`
	Seed   byte   `json:"seed"`
}

// FaultKind names an RPC fault injected before a step's Poll
type FaultKind string

const (
	FaultNone          FaultKind = ""
	FaultRangeTooLarge FaultKind = "range_too_large"
	FaultTransient     FaultKind = "transient"
	// FaultMalformedNoise injects the same error as FaultTransient
	FaultMalformedNoise FaultKind = "malformed"
)

// Step specifies chain-state changes before the next Poll and whether that Poll should
// fail
type Step struct {
	Description string       `json:"description"`
	Headers     []HeaderSpec `json:"headers,omitempty"`
	// RemoveLogsFrom drops prior logs at or above this block before adding step.Logs,
	// modeling reorg removal
	RemoveLogsFrom *uint64   `json:"removeLogsFrom,omitempty"`
	Logs           []LogSpec `json:"logs,omitempty"`
	Head           uint64    `json:"head"`
	// Fault makes the first FilterLogs call fail; the same Poll can recover within the
	// indexer's retry budget (see FakeSource.FilterLogsErrors)
	Fault FaultKind `json:"fault,omitempty"`
	// ExpectPollError requires Poll to fail, for example on a deep reorg; failure passes
	// and success becomes a report error
	ExpectPollError bool `json:"expectPollError,omitempty"`
}

// Scenario is a complete scripted replay dataset
type Scenario struct {
	ChainID     int64    `json:"chainId"`
	Addresses   []string `json:"addresses"`
	StartBlock  uint64   `json:"startBlock"`
	MaxLogRange uint64   `json:"maxLogRange,omitempty"`
	Steps       []Step   `json:"steps"`
}

// LoadScenario reads JSON; this package cannot export recorded testnet history into
// that format
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
