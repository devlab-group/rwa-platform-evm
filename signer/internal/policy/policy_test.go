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

func safePolicy(safeBlock string) string {
	return `{
		"chainId": "31337",
		"controller": "0x5FbDB2315678afecb367f032d93F642f64180aa3",
		"vault": "0xe7f1725E7734CE288F8367e1Bb143E90bb3F0512",
		"auditor": "0x9a7F3dfDEeBb51eAbBbA3c8eCCf8cF2a983A1C72",
		"projectId": "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61",
		"profileDigest": "0x1111111111111111111111111111111111111111111111111111111111111111",
		"safe": ` + safeBlock + `
	}`
}

func TestLoad_SafePolicy(t *testing.T) {
	pol, err := Load(writePolicyFile(t, safePolicy(`{
		"owners": ["0x70997970C51812dc3A010C7d01b50e0d17dc79C8", "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"],
		"threshold": 2
	}`)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if pol.Safe == nil || pol.Safe.Threshold != 2 || len(pol.Safe.Owners) != 2 {
		t.Fatalf("Safe = %+v", pol.Safe)
	}
	if !pol.Safe.IsOwner(pol.Safe.Owners[1]) {
		t.Error("IsOwner(listed owner) = false")
	}
}

func TestLoad_RejectsInvalidSafe(t *testing.T) {
	const a, b = `"0x70997970C51812dc3A010C7d01b50e0d17dc79C8"`, `"0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"`
	cases := map[string]string{
		"threshold above owners": `{"owners": [` + a + `], "threshold": 2}`,
		"zero threshold":         `{"owners": [` + a + `], "threshold": 0}`,
		"no owners":              `{"owners": [], "threshold": 1}`,
		"duplicate owner":        `{"owners": [` + a + `, ` + a + `], "threshold": 1}`,
		"owner not an address":   `{"owners": ["not-an-address", ` + b + `], "threshold": 1}`,
		"unknown field":          `{"owners": [` + a + `], "threshold": 1, "modules": []}`,
	}
	for name, block := range cases {
		if _, err := Load(writePolicyFile(t, safePolicy(block))); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	nonAddressAuditor := `{"chainId": "31337", "controller": "0x5FbDB2315678afecb367f032d93F642f64180aa3",
		"vault": "0xe7f1725E7734CE288F8367e1Bb143E90bb3F0512", "auditor": "safe.eth",
		"projectId": "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61", "profileDigest": "0x11",
		"safe": {"owners": [` + a + `], "threshold": 1}}`
	if _, err := Load(writePolicyFile(t, nonAddressAuditor)); err == nil {
		t.Error("non-address auditor with safe: expected an error")
	}
}
