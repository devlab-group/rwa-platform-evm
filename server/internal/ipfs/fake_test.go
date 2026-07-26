package ipfs

import (
	"context"
	"testing"

	"github.com/rwa-platform/server/internal/auditpkg"
)

func TestFakeClientCIDMatchesAuditpkgConvention(t *testing.T) {
	canonical, err := auditpkg.Canonicalize([]byte(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	wantCID, err := auditpkg.CIDv1Raw(canonical)
	if err != nil {
		t.Fatal(err)
	}

	client := NewFakeClient()
	gotCID, err := client.AddRaw(context.Background(), canonical)
	if err != nil {
		t.Fatal(err)
	}
	if gotCID != wantCID {
		t.Errorf("AddRaw CID = %s, want %s (auditpkg.CIDv1Raw)", gotCID, wantCID)
	}

	if err := client.Pin(context.Background(), gotCID); err != nil {
		t.Errorf("Pin: %v", err)
	}

	got, err := client.Get(context.Background(), gotCID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(canonical) {
		t.Errorf("Get returned different bytes")
	}
}

func TestFakeClientPinUnknownCIDFails(t *testing.T) {
	client := NewFakeClient()
	if err := client.Pin(context.Background(), "bafkunknown"); err == nil {
		t.Fatal("expected error pinning unknown CID")
	}
}
