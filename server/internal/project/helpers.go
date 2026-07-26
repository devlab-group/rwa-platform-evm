package project

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

func hexToBytes32(s string) ([32]byte, error) {
	var out [32]byte
	trimmed := strings.TrimPrefix(s, "0x")
	if len(trimmed) != 64 {
		return out, fmt.Errorf("expected 32-byte hex (64 chars after 0x), got %d chars", len(trimmed))
	}
	b, err := hex.DecodeString(trimmed)
	if err != nil {
		return out, err
	}
	copy(out[:], b)
	return out, nil
}

// projectIDToBytes32 derives ProjectConfig.projectId from the platform's
// UUID-string project identifier via keccak256(bytes(projectId)), the same
// convention as recordKey = keccak256(bytes(recordId)) (shared/eip712/types.md).
// This derivation is NOT explicitly specified in docs/spec/contracts.md /
// IRWAFactory.sol beyond "bytes32 projectId"; flagged for lead confirmation.
func projectIDToBytes32(projectID string) ([32]byte, error) {
	if projectID == "" {
		return [32]byte{}, fmt.Errorf("projectId must not be empty")
	}
	return crypto.Keccak256Hash([]byte(projectID)), nil
}

func parseBigInt(s string) (*big.Int, error) {
	if s == "" {
		return big.NewInt(0), nil
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("invalid decimal integer %q", s)
	}
	return n, nil
}
