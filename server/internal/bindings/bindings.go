// Package bindings encodes calldata and decodes events for the contracts in
// contracts/src.
//
// The bindings are hand-written, not abigen output. Each one builds a
// go-ethereum abi.ABI from a JSON fragment copied from the Solidity
// interfaces in contracts/src/interfaces, so field order and types must be kept
// in step with them by hand. cmd/verifyabi compares the selectors and event
// topics with the compiled artifacts in contracts/out.
package bindings

import (
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

// mustABI parses a JSON ABI fragment and panics on error. The fragments are
// compile-time constants, so a parse failure is a programming error
func mustABI(jsonABI string) abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(jsonABI))
	if err != nil {
		panic("bindings: invalid ABI literal: " + err.Error())
	}
	return parsed
}
