package bindings

import (
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

const complianceRegistryABIJSON = `[
  {"type":"function","name":"setStatus","stateMutability":"nonpayable",
    "inputs":[{"name":"account","type":"address"},{"name":"status","type":"uint8"},{"name":"validUntil","type":"uint64"}],
    "outputs":[]},
  {"type":"function","name":"setStatuses","stateMutability":"nonpayable",
    "inputs":[{"name":"accounts","type":"address[]"},{"name":"statuses","type":"uint8[]"},{"name":"validUntil","type":"uint64[]"}],
    "outputs":[]},
  {"type":"function","name":"isAllowed","stateMutability":"view",
    "inputs":[{"name":"account","type":"address"}],
    "outputs":[{"name":"","type":"bool"}]},
  {"type":"function","name":"getRecord","stateMutability":"view",
    "inputs":[{"name":"account","type":"address"}],
    "outputs":[{"name":"","type":"tuple","components":[
      {"name":"status","type":"uint8"},{"name":"validUntil","type":"uint64"}]}]},
  {"type":"function","name":"isSystemAddress","stateMutability":"view",
    "inputs":[{"name":"account","type":"address"}],
    "outputs":[{"name":"","type":"bool"}]},
  {"type":"event","name":"StatusChanged","anonymous":false,"inputs":[
    {"name":"account","type":"address","indexed":true},
    {"name":"previousStatus","type":"uint8","indexed":false},
    {"name":"newStatus","type":"uint8","indexed":false},
    {"name":"previousValidUntil","type":"uint64","indexed":false},
    {"name":"newValidUntil","type":"uint64","indexed":false},
    {"name":"caller","type":"address","indexed":true}]}
]`

// ComplianceRegistry encodes calldata / decodes events for IComplianceRegistry.
type ComplianceRegistry struct{ ABI abi.ABI }

// NewComplianceRegistry parses the ComplianceRegistry ABI once.
func NewComplianceRegistry() ComplianceRegistry {
	return ComplianceRegistry{ABI: mustABI(complianceRegistryABIJSON)}
}

// ComplianceStatus mirrors IComplianceRegistry.ComplianceStatus's uint8 encoding.
type ComplianceStatus uint8

const (
	ComplianceStatusUnknown ComplianceStatus = 0
	ComplianceStatusAllowed ComplianceStatus = 1
	ComplianceStatusBlocked ComplianceStatus = 2
)

// PackSetStatus builds calldata for setStatus(address,uint8,uint64).
func (c ComplianceRegistry) PackSetStatus(account common.Address, status ComplianceStatus, validUntil uint64) ([]byte, error) {
	return c.ABI.Pack("setStatus", account, uint8(status), validUntil)
}

// PackSetStatuses builds calldata for setStatuses(address[],uint8[],uint64[]).
func (c ComplianceRegistry) PackSetStatuses(accounts []common.Address, statuses []ComplianceStatus, validUntils []uint64) ([]byte, error) {
	rawStatuses := make([]uint8, len(statuses))
	for i, s := range statuses {
		rawStatuses[i] = uint8(s)
	}
	return c.ABI.Pack("setStatuses", accounts, rawStatuses, validUntils)
}

// PackIsAllowed builds calldata for the isAllowed(address) view call.
func (c ComplianceRegistry) PackIsAllowed(account common.Address) ([]byte, error) {
	return c.ABI.Pack("isAllowed", account)
}

// UnpackIsAllowed decodes the return value of isAllowed.
func (c ComplianceRegistry) UnpackIsAllowed(data []byte) (bool, error) {
	out, err := c.ABI.Unpack("isAllowed", data)
	if err != nil {
		return false, err
	}
	return *abi.ConvertType(out[0], new(bool)).(*bool), nil
}

// ComplianceRecord mirrors IComplianceRegistry.ComplianceRecord.
type ComplianceRecord struct {
	Status     uint8
	ValidUntil uint64
}

// PackGetRecord builds calldata for the getRecord(address) view call.
func (c ComplianceRegistry) PackGetRecord(account common.Address) ([]byte, error) {
	return c.ABI.Pack("getRecord", account)
}

// UnpackGetRecord decodes the return value of getRecord.
func (c ComplianceRegistry) UnpackGetRecord(data []byte) (ComplianceRecord, error) {
	out, err := c.ABI.Unpack("getRecord", data)
	if err != nil {
		return ComplianceRecord{}, err
	}
	rec := abi.ConvertType(out[0], new(ComplianceRecord)).(*ComplianceRecord)
	return *rec, nil
}

// StatusChangedEvent mirrors the StatusChanged event's non-indexed data.
type StatusChangedEvent struct {
	Account            common.Address
	PreviousStatus     uint8
	NewStatus          uint8
	PreviousValidUntil uint64
	NewValidUntil      uint64
	Caller             common.Address
}

// UnpackStatusChanged decodes a StatusChanged log's data (non-indexed
// fields) and topics (indexed fields: account, caller).
func (c ComplianceRegistry) UnpackStatusChanged(data []byte, topics []common.Hash) (StatusChangedEvent, error) {
	var ev StatusChangedEvent
	if err := c.ABI.UnpackIntoInterface(&ev, "StatusChanged", data); err != nil {
		return StatusChangedEvent{}, err
	}
	if len(topics) >= 3 {
		ev.Account = common.HexToAddress(topics[1].Hex())
		ev.Caller = common.HexToAddress(topics[2].Hex())
	}
	return ev, nil
}

// EventID returns the keccak256 topic0 for the named event, used by the indexer's log filter.
func (c ComplianceRegistry) EventID(name string) common.Hash {
	return c.ABI.Events[name].ID
}
