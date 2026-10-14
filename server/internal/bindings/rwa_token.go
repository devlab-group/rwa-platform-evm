package bindings

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// rwaTokenABIJSON declares the ERC-7943 (uRWA) enforcement events RWAToken
// emits. Frozen is emitted when an admin sets a holder's absolute frozen amount
// and when a forced transfer reduces it before moving tokens. ForcedTransfer is
// emitted with the ordinary ERC-20 Transfer of a seizure. Addresses are indexed
// and the amount is in log.Data. The signatures and topic0 hashes are pinned in
// shared/vectors/erc7943-abi.json, and erc7943_vectors_test.go checks this
// literal against it.
//
// The standard's read functions (canSend, canReceive, canTransfer,
// getFrozenTokens) are not declared because the server derives frozen state by
// replaying these events.
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

// FrozenEvent mirrors the Frozen event: the holder whose absolute frozen
// amount was overwritten (indexed) and the new amount (non-indexed).
type FrozenEvent struct {
	Account common.Address
	Amount  *big.Int
}

// UnpackFrozen decodes a Frozen log. A log missing its indexed account topic
// is malformed rather than a holder of the zero address, so it errors out and
// the indexer dead-letters it instead of projecting a bogus freeze.
func (t RWAToken) UnpackFrozen(data []byte, topics []common.Hash) (FrozenEvent, error) {
	var partial struct{ Amount *big.Int }
	if err := t.ABI.UnpackIntoInterface(&partial, "Frozen", data); err != nil {
		return FrozenEvent{}, err
	}
	if len(topics) < 2 {
		return FrozenEvent{}, fmt.Errorf("bindings: Frozen log has %d topics, want 2", len(topics))
	}
	return FrozenEvent{Account: common.HexToAddress(topics[1].Hex()), Amount: partial.Amount}, nil
}

// ForcedTransferEvent mirrors the ForcedTransfer event: seized-from and
// seized-to (both indexed) and the amount moved (non-indexed).
type ForcedTransferEvent struct {
	From   common.Address
	To     common.Address
	Amount *big.Int
}

// UnpackForcedTransfer decodes a ForcedTransfer log, erroring on a log that
// does not carry both indexed address topics.
func (t RWAToken) UnpackForcedTransfer(data []byte, topics []common.Hash) (ForcedTransferEvent, error) {
	var partial struct{ Amount *big.Int }
	if err := t.ABI.UnpackIntoInterface(&partial, "ForcedTransfer", data); err != nil {
		return ForcedTransferEvent{}, err
	}
	if len(topics) < 3 {
		return ForcedTransferEvent{}, fmt.Errorf("bindings: ForcedTransfer log has %d topics, want 3", len(topics))
	}
	return ForcedTransferEvent{
		From:   common.HexToAddress(topics[1].Hex()),
		To:     common.HexToAddress(topics[2].Hex()),
		Amount: partial.Amount,
	}, nil
}
