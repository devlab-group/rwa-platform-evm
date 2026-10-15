// Package hardware defines digest-signing adapters; recognized devices use StubAdapter,
// whose signing methods return ErrNotSupported
package hardware

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

// ErrNotSupported marks unimplemented Address and SignDigest operations
var ErrNotSupported = errors.New("hardware: no hardware signer is implemented in this build")

// Signer requires device-side signing of raw 32-byte digests into 65-byte ECDSA
// signatures without exposing private keys to the host
type Signer interface {
	// Address returns the connected device's signing address
	Address() (common.Address, error)
	// SignDigest signs a raw EIP-712 digest as 65-byte [R || S || V] with V=27/28
	SignDigest(digest [32]byte) ([]byte, error)
	// Close releases device handles
	Close() error
}

// StubAdapter supports the CLI's --hardware path without device libraries;
// Address/SignDigest return ErrNotSupported and Close succeeds
type StubAdapter struct{}

var _ Signer = StubAdapter{}

func (StubAdapter) Address() (common.Address, error)      { return common.Address{}, ErrNotSupported }
func (StubAdapter) SignDigest(_ [32]byte) ([]byte, error) { return nil, ErrNotSupported }
func (StubAdapter) Close() error                          { return nil }

// DeviceKind names a wallet/HSM family. Open supplies its Signer adapter; adapters
// receive precomputed digests rather than typed data, keeping device integration behind
// that interface.
type DeviceKind string

const (
	DeviceLedger  DeviceKind = "ledger"
	DeviceTrezor  DeviceKind = "trezor"
	DeviceYubiKey DeviceKind = "yubikey"
	DeviceHSM     DeviceKind = "hsm"
)

// SupportedDeviceKinds lists every DeviceKind Open recognizes, for usage text and
// validation
var SupportedDeviceKinds = []DeviceKind{DeviceLedger, DeviceTrezor, DeviceYubiKey, DeviceHSM}

// Open returns StubAdapter for recognized kinds and a distinct error for unknown
// --hardware names; see StubAdapter for operation behavior
func Open(kind DeviceKind) (Signer, error) {
	for _, k := range SupportedDeviceKinds {
		if kind == k {
			return StubAdapter{}, nil
		}
	}
	return nil, fmt.Errorf("hardware: unknown device kind %q (supported: %v)", kind, SupportedDeviceKinds)
}
