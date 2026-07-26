package hardware

import (
	"errors"
	"testing"
)

func TestStubAdapter_AlwaysReportsNotSupported(t *testing.T) {
	var s Signer = StubAdapter{}
	if _, err := s.Address(); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Address err = %v, want ErrNotSupported", err)
	}
	if _, err := s.SignDigest([32]byte{}); !errors.Is(err, ErrNotSupported) {
		t.Errorf("SignDigest err = %v, want ErrNotSupported", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close err = %v, want nil", err)
	}
}

// TestOpen_SupportedKindsReturnStubAdapter covers every DeviceKind Open
// recognizes: each is a real, distinct integration seam (see
// docs/adr/ADR-006-hardware-wallet-signing.md), and every one currently
// resolves to the same not-yet-implemented StubAdapter, never a silent
// fallback to software signing.
func TestOpen_SupportedKindsReturnStubAdapter(t *testing.T) {
	for _, kind := range SupportedDeviceKinds {
		s, err := Open(kind)
		if err != nil {
			t.Fatalf("Open(%q): %v", kind, err)
		}
		if _, err := s.Address(); !errors.Is(err, ErrNotSupported) {
			t.Errorf("Open(%q).Address() err = %v, want ErrNotSupported", kind, err)
		}
	}
}

func TestOpen_UnknownKindIsARejectionNotErrNotSupported(t *testing.T) {
	_, err := Open(DeviceKind("made-up-device"))
	if err == nil {
		t.Fatal("expected an error for an unrecognized device kind")
	}
	if errors.Is(err, ErrNotSupported) {
		t.Error("an unrecognized --hardware value must not be reported the same way as a recognized-but-unimplemented device")
	}
}
