// Package hardware defines the adapter interface for hardware-wallet signing. The
// package ships only StubAdapter, which reports ErrNotSupported for every device kind.
package hardware

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

// ErrNotSupported is returned by every method of an adapter with no working device
// integration
var ErrNotSupported = errors.New("hardware: no hardware signer is implemented in this build")

// Signer is implemented by a hardware-wallet adapter that signs a raw 32-byte digest
// with a standard 65-byte ECDSA signature without exposing the private key to the host
type Signer interface {
	// Address returns the signing address exposed by the connected device.
	Address() (common.Address, error)
	// SignDigest signs the raw 32-byte EIP-712 digest and returns a
	// standard 65-byte [R || S || V] signature with V in {27,28}.
	SignDigest(digest [32]byte) ([]byte, error)
	// Close releases any device handle held by the adapter.
	Close() error
}

// StubAdapter implements Signer but always reports ErrNotSupported. It lets cmd/signer
// offer a --hardware flag without depending on a device library, and a real adapter can
// replace it as another Signer implementation.
type StubAdapter struct{}

var _ Signer = StubAdapter{}

func (StubAdapter) Address() (common.Address, error)      { return common.Address{}, ErrNotSupported }
func (StubAdapter) SignDigest(_ [32]byte) ([]byte, error) { return nil, ErrNotSupported }
func (StubAdapter) Close() error                          { return nil }

// DeviceKind identifies a hardware-wallet or HSM device family that could back a Signer
// adapter. Supporting a device means returning an adapter that implements Signer from
// Open. Nothing in cmd/signer or internal/attestation changes, because every adapter
// signs only the precomputed 32-byte digest (SignDigest), never the typed data.
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

// Open returns a Signer adapter for kind. Every recognized kind returns StubAdapter,
// whose methods all return ErrNotSupported. An unrecognized kind is a different error,
// meaning the --hardware value is mistyped rather than the device unimplemented.
func Open(kind DeviceKind) (Signer, error) {
	for _, k := range SupportedDeviceKinds {
		if kind == k {
			return StubAdapter{}, nil
		}
	}
	return nil, fmt.Errorf("hardware: unknown device kind %q (supported: %v)", kind, SupportedDeviceKinds)
}
