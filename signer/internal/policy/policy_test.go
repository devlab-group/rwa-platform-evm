package policy

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePolicyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLoad_ValidPolicy(t *testing.T) {
	path := writePolicyFile(t, `{
		"chainId": "31337",
		"controller": "0x5FbDB2315678afecb367f032d93F642f64180aa3",
		"vault": "0xe7f1725E7734CE288F8367e1Bb143E90bb3F0512",
		"auditor": "0x1111111111111111111111111111111111111111",
		"projectId": "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61",
		"profileDigest": "0x1111111111111111111111111111111111111111111111111111111111111"
	}`)
	pol, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if pol.ChainID != "31337" {
		t.Errorf("ChainID = %q", pol.ChainID)
	}
	if pol.MaxAttestationLifetime != DefaultMaxAttestationLifetime {
		t.Errorf("MaxAttestationLifetime = %v, want default %v", pol.MaxAttestationLifetime, DefaultMaxAttestationLifetime)
	}
}

func TestLoad_CustomLifetime(t *testing.T) {
	path := writePolicyFile(t, `{
		"chainId": "31337", "controller": "0xabc", "vault": "0xdef",
		"auditor": "0x111", "projectId": "p1", "profileDigest": "0xdigest",
		"maxAttestationLifetimeHours": 12
	}`)
	pol, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if pol.MaxAttestationLifetime != 12*time.Hour {
		t.Errorf("MaxAttestationLifetime = %v, want 12h", pol.MaxAttestationLifetime)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected error for missing policy file")
	}
}

func TestLoad_RejectsUnknownField(t *testing.T) {
	path := writePolicyFile(t, `{
		"chainId": "31337", "controller": "0xabc", "vault": "0xdef",
		"auditor": "0x111", "projectId": "p1", "profileDigest": "0xdigest",
		"surprise": true
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected rejection of an unknown field")
	}
}

func TestLoad_RejectsMissingRequiredFields(t *testing.T) {
	for _, missing := range []string{"chainId", "controller", "vault", "auditor", "projectId", "profileDigest"} {
		fields := map[string]string{
			"chainId": "31337", "controller": "0xabc", "vault": "0xdef",
			"auditor": "0x111", "projectId": "p1", "profileDigest": "0xdigest",
		}
		delete(fields, missing)
		content := "{"
		first := true
		for k, v := range fields {
			if !first {
				content += ","
			}
			first = false
			content += `"` + k + `":"` + v + `"`
		}
		content += "}"
		path := writePolicyFile(t, content)
		if _, err := Load(path); err == nil {
			t.Errorf("expected rejection with %q missing", missing)
		}
	}
}

func TestLoad_RejectsNonNumericChainID(t *testing.T) {
	path := writePolicyFile(t, `{
		"chainId": "not-a-number", "controller": "0xabc", "vault": "0xdef",
		"auditor": "0x111", "projectId": "p1", "profileDigest": "0xdigest"
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected rejection of a non-numeric chainId")
	}
}

func TestLoad_RejectsNegativeLifetime(t *testing.T) {
	path := writePolicyFile(t, `{
		"chainId": "31337", "controller": "0xabc", "vault": "0xdef",
		"auditor": "0x111", "projectId": "p1", "profileDigest": "0xdigest",
		"maxAttestationLifetimeHours": -1
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected rejection of a negative maxAttestationLifetimeHours")
	}
}

func TestLoad_RejectsTrailingData(t *testing.T) {
	path := writePolicyFile(t, `{
		"chainId": "31337", "controller": "0xabc", "vault": "0xdef",
		"auditor": "0x111", "projectId": "p1", "profileDigest": "0xdigest"
	}{}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected rejection of trailing data after the JSON object")
	}
}

func TestLoad_RejectsMalformedJSON(t *testing.T) {
	path := writePolicyFile(t, `{not json`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected rejection of malformed JSON")
	}
}
