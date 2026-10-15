// Package bindings encodes contract calldata and decodes events for contracts/src using
// hand-written go-ethereum ABI fragments. Keep field order and types aligned with
// Solidity interfaces and concrete-contract additions. cmd/verifyabi compares selectors
// and event topics against contracts/out artifacts.
package bindings

import (
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

// mustABI parses a constant JSON ABI fragment, panicking on malformed program data
func mustABI(jsonABI string) abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(jsonABI))
	if err != nil {
		panic("bindings: invalid ABI literal: " + err.Error())
	}
	return parsed
}
