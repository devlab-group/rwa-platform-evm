package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNew_ValidFields(t *testing.T) {
	r, err := New(
		"0x"+strings.Repeat("ab", 20),
		"MintAttestation",
		"0x"+strings.Repeat("cd", 32),
		"0x"+strings.Repeat("ef", 65),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.FormatVersion != "1.0" {
		t.Errorf("FormatVersion = %q", r.FormatVersion)
	}
}

func TestNew_RejectsBadAuditor(t *testing.T) {
	_, err := New("not-an-address", "MintAttestation", "0x"+strings.Repeat("cd", 32), "0x"+strings.Repeat("ef", 65))
	if err == nil {
		t.Fatal("expected error for malformed auditor")
	}
}

func TestNew_RejectsBadPrimaryType(t *testing.T) {
	_, err := New("0x"+strings.Repeat("ab", 20), "TransferAttestation", "0x"+strings.Repeat("cd", 32), "0x"+strings.Repeat("ef", 65))
	if err == nil {
		t.Fatal("expected error for invalid primaryType")
	}
}

func TestNew_RejectsBadSignatureLength(t *testing.T) {
	_, err := New("0x"+strings.Repeat("ab", 20), "MintAttestation", "0x"+strings.Repeat("cd", 32), "0x1234")
	if err == nil {
		t.Fatal("expected error for short signature")
	}
}

func TestWrite_ProducesSchemaConformantJSON(t *testing.T) {
	r, err := New("0x"+strings.Repeat("ab", 20), "BurnAttestation", "0x"+strings.Repeat("cd", 32), "0x"+strings.Repeat("ef", 65))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "signed-result.json")
	if err := Write(path, r); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"formatVersion", "auditor", "primaryType", "typedDataDigest", "signature", "signedAt"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in signed-result.json", key)
		}
	}
	if len(m) != 6 {
		t.Errorf("signed-result.json has %d keys, want exactly 6 (additionalProperties:false)", len(m))
	}
}
