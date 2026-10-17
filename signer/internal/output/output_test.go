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

func TestNew_AcceptsConcatenatedSafeSignatures(t *testing.T) {
	if _, err := New("0x"+strings.Repeat("ab", 20), "MintAttestation", "0x"+strings.Repeat("cd", 32), "0x"+strings.Repeat("ef", 130)); err != nil {
		t.Fatalf("two concatenated signatures: %v", err)
	}
	if _, err := New("0x"+strings.Repeat("ab", 20), "MintAttestation", "0x"+strings.Repeat("cd", 32), "0x"+strings.Repeat("ef", 100)); err == nil {
		t.Fatal("expected error for a signature that is not a multiple of 65 bytes")
	}
}

func TestShare_RoundTripAndStrictRead(t *testing.T) {
	addr, hash := "0x"+strings.Repeat("ab", 20), "0x"+strings.Repeat("cd", 32)
	s, err := NewShare(addr, "31337", addr, "BurnAttestation", hash, hash, "0x"+strings.Repeat("ef", 65))
	if err != nil {
		t.Fatalf("NewShare: %v", err)
	}
	path := filepath.Join(t.TempDir(), "share.json")
	if err := WriteShare(path, s); err != nil {
		t.Fatalf("WriteShare: %v", err)
	}
	got, err := ReadShare(path)
	if err != nil || got != s {
		t.Fatalf("ReadShare = %+v, %v; want %+v", got, err, s)
	}

	if _, err := NewShare(addr, "31337", addr, "MintAttestation", hash, hash, "0x"+strings.Repeat("ef", 130)); err == nil {
		t.Error("expected error for a share carrying two signatures")
	}
	extra := strings.Replace(mustRead(t, path), `"formatVersion"`, `"extra": 1, "formatVersion"`, 1)
	if err := os.WriteFile(path, []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadShare(path); err == nil {
		t.Error("expected error for an unknown field")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each field of a signed result and a share is checked on its own, and a record that
// fails validation never reaches the disk
func TestValidate_RejectsEachBadField(t *testing.T) {
	addr, hash, sig := "0x"+strings.Repeat("ab", 20), "0x"+strings.Repeat("cd", 32), "0x"+strings.Repeat("ef", 65)
	validResult := func() SignedResult {
		r, err := New(addr, "MintAttestation", hash, sig)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	validShare := func() SignatureShare {
		s, err := NewShare(addr, "31337", addr, "MintAttestation", hash, hash, sig)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	results := []struct {
		name    string
		edit    func(*SignedResult)
		wantErr string
	}{
		{"formatVersion", func(r *SignedResult) { r.FormatVersion = "2.0" }, "formatVersion"},
		{"auditor", func(r *SignedResult) { r.Auditor = "0x1234" }, "auditor"},
		{"primaryType", func(r *SignedResult) { r.PrimaryType = "Mint" }, "primaryType"},
		{"digest", func(r *SignedResult) { r.TypedDataDigest = "0x12" }, "typedDataDigest"},
		{"signature", func(r *SignedResult) { r.Signature = "0x12" }, "signature"},
		{"signedAt", func(r *SignedResult) { r.SignedAt = "yesterday" }, "signedAt"},
	}
	for _, tt := range results {
		t.Run("result/"+tt.name, func(t *testing.T) {
			r := validResult()
			tt.edit(&r)
			err := r.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate error = %v, want it to mention %q", err, tt.wantErr)
			}
			path := filepath.Join(t.TempDir(), "signed-result.json")
			if err := Write(path, r); err == nil {
				t.Error("Write accepted an invalid result")
			}
			if _, err := os.Stat(path); err == nil {
				t.Error("Write left a file behind for an invalid result")
			}
		})
	}

	shares := []struct {
		name    string
		edit    func(*SignatureShare)
		wantErr string
	}{
		{"formatVersion", func(s *SignatureShare) { s.FormatVersion = "2.0" }, "formatVersion"},
		{"safe", func(s *SignatureShare) { s.Safe = "0x1234" }, "safe and owner"},
		{"owner", func(s *SignatureShare) { s.Owner = "0x1234" }, "safe and owner"},
		{"chainId zero", func(s *SignatureShare) { s.ChainID = "0" }, "chainId"},
		{"chainId leading zero", func(s *SignatureShare) { s.ChainID = "01" }, "chainId"},
		{"chainId negative", func(s *SignatureShare) { s.ChainID = "-1" }, "chainId"},
		{"primaryType", func(s *SignatureShare) { s.PrimaryType = "Mint" }, "primaryType"},
		{"typedDataDigest", func(s *SignatureShare) { s.TypedDataDigest = "0x12" }, "typedDataDigest"},
		{"safeMessageHash", func(s *SignatureShare) { s.SafeMessageHash = "0x12" }, "safeMessageHash"},
		{"signedAt", func(s *SignatureShare) { s.SignedAt = "yesterday" }, "signedAt"},
	}
	for _, tt := range shares {
		t.Run("share/"+tt.name, func(t *testing.T) {
			s := validShare()
			tt.edit(&s)
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate error = %v, want it to mention %q", err, tt.wantErr)
			}
			path := filepath.Join(t.TempDir(), "share.json")
			if err := WriteShare(path, s); err == nil {
				t.Error("WriteShare accepted an invalid share")
			}
			if _, err := os.Stat(path); err == nil {
				t.Error("WriteShare left a file behind for an invalid share")
			}
		})
	}
}

func TestReadShare_RefusesTrailingDataAndMissingFile(t *testing.T) {
	addr, hash := "0x"+strings.Repeat("ab", 20), "0x"+strings.Repeat("cd", 32)
	s, err := NewShare(addr, "31337", addr, "MintAttestation", hash, hash, "0x"+strings.Repeat("ef", 65))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "share.json")
	if err := WriteShare(path, s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(mustRead(t, path)+`{"second":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadShare(path); err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Errorf("ReadShare with trailing data error = %v", err)
	}
	if _, err := ReadShare(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("ReadShare accepted a missing file")
	}
	bad := `{"formatVersion":"1.0","safe":"x","chainId":"1","owner":"x","primaryType":"MintAttestation","typedDataDigest":"x","safeMessageHash":"x","signature":"x","signedAt":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadShare(path); err == nil || !strings.Contains(err.Error(), "safe and owner") {
		t.Errorf("ReadShare of an invalid share error = %v, want validation error", err)
	}
}
