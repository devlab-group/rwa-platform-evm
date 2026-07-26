// Package hardware defines the adapter interface for hardware-wallet
// signing. Support is adapter-based and can initially target a single tested
// device family. This build ships only a stub adapter; real device support
// can land later.
package hardware

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

// ErrNotSupported is returned by every method of an adapter that has no
// working device integration in this build.
var ErrNotSupported = errors.New("hardware: no hardware signer is implemented in this build")

// Signer is implemented by a hardware-wallet adapter capable of producing
// standard 65-byte ECDSA signatures over a raw 32-byte digest, without ever
// exposing the private key to the host.
type Signer interface {
	// Address returns the signing address exposed by the connected device.
	Address() (common.Address, error)
	// SignDigest signs the raw 32-byte EIP-712 digest and returns a
	// standard 65-byte [R || S || V] signature with V in {27,28}.
	SignDigest(digest [32]byte) ([]byte, error)
	// Close releases any device handle held by the adapter.
	Close() error
}

// StubAdapter implements Signer but always reports ErrNotSupported. It
// exists so cmd/ can wire a --hardware flag today without depending on any
// concrete device library, and so a future device adapter is a drop-in
// Signer implementation.
type StubAdapter struct{}

var _ Signer = StubAdapter{}

func (StubAdapter) Address() (common.Address, error)      { return common.Address{}, ErrNotSupported }
func (StubAdapter) SignDigest(_ [32]byte) ([]byte, error) { return nil, ErrNotSupported }
func (StubAdapter) Close() error                          { return nil }

// DeviceKind identifies a hardware-wallet/HSM device family the signer
// could integrate with as a Signer adapter (see
// docs/adr/ADR-006-hardware-wallet-signing.md). Naming --hardware's value
// after a specific device, rather than leaving it a bare boolean, is the
// seam a real adapter plugs into: adding a device means adding one case to
// Open and one adapter type that implements Signer -- nothing in cmd/signer
// or internal/attestation needs to change, since every adapter signs only
// the pre-computed 32-byte EIP-712 digest (SignDigest), never the typed
// data itself.
type DeviceKind string

const (
	DeviceLedger  DeviceKind = "ledger"
	DeviceTrezor  DeviceKind = "trezor"
	DeviceYubiKey DeviceKind = "yubikey"
	DeviceHSM     DeviceKind = "hsm"
)

// SupportedDeviceKinds lists every DeviceKind Open recognizes, for usage
// text and validation.
var SupportedDeviceKinds = []DeviceKind{DeviceLedger, DeviceTrezor, DeviceYubiKey, DeviceHSM}

// Open returns a Signer adapter for kind. Every kind currently returns
// StubAdapter (every method ErrNotSupported): this build ships no real
// device integration, and it's deliberately not attempted here since a real
// device cannot be exercised in this environment -- but Open is where a real
// adapter is wired in later,
// without touching any caller. An unrecognized kind is a distinct error
// from ErrNotSupported: it means an operator mistyped --hardware, not that
// the (correctly named) device is merely unimplemented yet.
func Open(kind DeviceKind) (Signer, error) {
	for _, k := range SupportedDeviceKinds {
		if kind == k {
			return StubAdapter{}, nil
		}
	}
	return nil, fmt.Errorf("hardware: unknown device kind %q (supported: %v)", kind, SupportedDeviceKinds)
}
