package api

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// hexBytes decodes an optionally "0x"-prefixed hex string to raw bytes.
func hexBytes(s string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(s, "0x"))
}

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
