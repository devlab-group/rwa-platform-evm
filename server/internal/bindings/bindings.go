// Package bindings hand-encodes calldata and decodes events for the
// contract interfaces in contracts/src/interfaces/*.sol.
//
// These are intentionally NOT abigen output: when this package was written
// the contracts had not yet been compiled (no contracts/out/*.json ABI
// artifacts existed), so it hand-builds go-ethereum abi.ABI values from the
// same function/event signatures declared in the Solidity interfaces. Field
// order and types were copied by hand from each interface file. A future
// pass should replace this package with abigen output once `forge build`
// runs, deleting this file — this is the pre-codegen stand-in.
package bindings

import (
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

// mustABI parses a Solidity-style JSON ABI fragment, panicking on error
// since these are compile-time constants; a malformed literal is a
// programmer error caught immediately by any test or binary that imports
// this package (every constructor below is exercised by bindings_test.go).
func mustABI(jsonABI string) abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(jsonABI))
	if err != nil {
		panic("bindings: invalid ABI literal: " + err.Error())
	}
	return parsed
}
