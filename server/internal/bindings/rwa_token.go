package bindings

import (
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// rwaTokenABIJSON declares the ERC-7943 (uRWA) enforcement events RWAToken
// emits: Frozen whenever an admin overwrites a holder's absolute frozen
// amount (including the reduction a forced transfer causes before it moves
// tokens), and ForcedTransfer alongside the ordinary ERC-20 Transfer of a
// seizure. Both carry their addresses in topics and the amount in data; the
// signatures and topic0 hashes are pinned in shared/vectors/erc7943-abi.json,
// which erc7943_vectors_test.go asserts this literal against.
//
// The read side of the standard (canSend/canReceive/canTransfer/
// getFrozenTokens) is absent on purpose: the server derives frozen state by
// replaying these events, so it never calls those getters.
const rwaTokenABIJSON = `[
  {"type":"event","name":"Frozen","anonymous":false,"inputs":[
    {"name":"account","type":"address","indexed":true},{"name":"amount","type":"uint256","indexed":false}]},
  {"type":"event","name":"ForcedTransfer","anonymous":false,"inputs":[
    {"name":"from","type":"address","indexed":true},{"name":"to","type":"address","indexed":true},
    {"name":"amount","type":"uint256","indexed":false}]}
]`

// RWAToken decodes the token's ERC-7943 enforcement events.
type RWAToken struct{ ABI abi.ABI }

// NewRWAToken parses the ERC-7943 event ABI once.
func NewRWAToken() RWAToken { return RWAToken{ABI: mustABI(rwaTokenABIJSON)} }

// EventID returns the keccak256 topic0 for the named event.
func (t RWAToken) EventID(name string) common.Hash { return t.ABI.Events[name].ID }
