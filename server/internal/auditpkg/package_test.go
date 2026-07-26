package auditpkg

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func hexZeros() string { return strings.Repeat("0", 64) }

func TestBuildAndOpenPackageRoundTrip(t *testing.T) {
	profileJSON := []byte(`{"profileVersion":"1.0"}`)
	metadataJSON := []byte(`{"recordId":"GOLD-1"}`)
	profileDigest := Digest(profileJSON)
	metadataDigest := Digest(metadataJSON)
	td := TypedDataDoc{
		PrimaryType: "MintAttestation",
		Domain:      TypedDataDomain{Name: "RWA-Supply-Attestation", Version: "1", ChainID: 31337, VerifyingContract: "0x5FbDB2315678afecb367f032d93F642f64180aa"},
		Message: TypedDataMessage{
			Auditor: "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266", ProfileDigest: "0x" + hexZeros(),
			RecordID: "GOLD-1", RecordKey: "0x" + hexZeros(), MetadataDigest: "0x" + hexZeros(),
			Amount: "1000", Nonce: "1", ValidUntil: 1800000000, Vault: "0x5FbDB2315678afecb367f032d93F642f64180aa",
		},
	}
	proofs := []ProofFile{{Name: "report.pdf", MIME: "application/pdf", Data: []byte("%PDF-fake")}}

	zipBytes, sha, err := BuildPackage("MintAttestation", profileDigest, metadataDigest, profileJSON, metadataJSON, td, proofs)
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if sha == "" {
		t.Fatal("empty package sha256")
	}

	opened, err := OpenPackage(zipBytes)
	if err != nil {
		t.Fatalf("OpenPackage: %v", err)
	}
	if opened.Manifest.PrimaryType != "MintAttestation" {
		t.Errorf("PrimaryType = %q", opened.Manifest.PrimaryType)
	}
	if string(opened.Files["profile.json"]) != string(profileJSON) {
		t.Errorf("profile.json mismatch")
	}
	if string(opened.Files["metadata.json"]) != string(metadataJSON) {
		t.Errorf("metadata.json mismatch")
	}
	if string(opened.Files["proofs/report.pdf"]) != "%PDF-fake" {
		t.Errorf("proofs/report.pdf mismatch")
	}
	if len(opened.Manifest.Files) != 4 {
		t.Errorf("manifest.Files len = %d, want 4", len(opened.Manifest.Files))
	}
}

func TestBuildPackageRejectsBadPrimaryType(t *testing.T) {
	_, _, err := BuildPackage("NotAType", [32]byte{}, [32]byte{}, []byte("{}"), []byte("{}"), TypedDataDoc{}, nil)
	if err == nil {
		t.Fatal("expected error for bad primaryType")
	}
}

func TestOpenPackageRejectsPathTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../../etc/passwd")
	w.Write([]byte("evil"))
	zw.Close()

	if _, err := OpenPackage(buf.Bytes()); err == nil {
		t.Fatal("expected error for path traversal entry")
	}
}

func TestOpenPackageRejectsDuplicateNames(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w1, _ := zw.Create("a.txt")
	w1.Write([]byte("one"))
	w2, _ := zw.Create("a.txt")
	w2.Write([]byte("two"))
	zw.Close()

	if _, err := OpenPackage(buf.Bytes()); err == nil {
		t.Fatal("expected error for duplicate entry name")
	}
}

func TestOpenPackageRejectsMissingManifest(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("profile.json")
	w.Write([]byte("{}"))
	zw.Close()

	if _, err := OpenPackage(buf.Bytes()); err == nil {
		t.Fatal("expected error for missing manifest.json")
	}
}

func TestOpenPackageRejectsHashMismatch(t *testing.T) {
	profileJSON := []byte(`{"a":1}`)
	metadataJSON := []byte(`{"b":2}`)
	zipBytes, _, err := BuildPackage("MintAttestation", Digest(profileJSON), Digest(metadataJSON), profileJSON, metadataJSON, TypedDataDoc{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Tamper: rebuild the zip with profile.json's content changed but
	// manifest.json (and its recorded hash) left untouched.
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		rc, _ := f.Open()
		var data bytes.Buffer
		data.ReadFrom(rc)
		rc.Close()
		content := data.Bytes()
		if f.Name == "profile.json" {
			content = []byte(`{"a":999}`)
		}
		w, _ := zw.Create(f.Name)
		w.Write(content)
	}
	zw.Close()

	if _, err := OpenPackage(out.Bytes()); err == nil {
		t.Fatal("expected error for tampered file / hash mismatch")
	}
}

// rawZip builds a .rwa-shaped zip archive directly from an entry-name ->
// content map, bypassing BuildPackage so a test can construct a
// deliberately non-conformant manifest.json.
func rawZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// validManifestEntries returns a self-consistent set of {manifest.json,
// profile.json, metadata.json, typed-data.json} zip entries (correct
// sha256/size for every manifest.files entry), so each test below can mutate
// exactly the one thing it's probing and still pass every OTHER check.
func validManifestEntries(t *testing.T) map[string]string {
	t.Helper()
	profile := `{"a":1}`
	metadata := `{"b":2}`
	typedData := `{"c":3}`
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	manifest := fmt.Sprintf(`{
		"packageVersion":"1.0","primaryType":"MintAttestation",
		"profileDigest":"0x%s","metadataDigest":"0x%s",
		"files":[
			{"path":"profile.json","sha256":"%s","size":%d,"mime":"application/json"},
			{"path":"metadata.json","sha256":"%s","size":%d,"mime":"application/json"},
			{"path":"typed-data.json","sha256":"%s","size":%d,"mime":"application/json"}
		]
	}`, hexZeros(), hexZeros(), sum(profile), len(profile), sum(metadata), len(metadata), sum(typedData), len(typedData))
	return map[string]string{
		"manifest.json":   manifest,
		"profile.json":    profile,
		"metadata.json":   metadata,
		"typed-data.json": typedData,
	}
}

// TestOpenPackageRejectsUndeclaredExtractedFile checks set-equality: an extra
// file present in the archive but never mentioned in manifest.json must be
// rejected, not silently accepted alongside the declared files.
func TestOpenPackageRejectsUndeclaredExtractedFile(t *testing.T) {
	entries := validManifestEntries(t)
	entries["proofs/hidden-payload.bin"] = "surprise"
	if _, err := OpenPackage(rawZip(t, entries)); err == nil {
		t.Fatal("expected error for an extracted file the manifest never declared")
	}
}

// TestOpenPackageRejectsDuplicateManifestPath pins "duplicate paths in
// manifest.files" rejection — a manifest can otherwise describe the same
// archive entry twice with two different claimed hashes/sizes.
func TestOpenPackageRejectsDuplicateManifestPath(t *testing.T) {
	entries := validManifestEntries(t)
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	profile := entries["profile.json"]
	entries["manifest.json"] = fmt.Sprintf(`{
		"packageVersion":"1.0","primaryType":"MintAttestation",
		"profileDigest":"0x%s","metadataDigest":"0x%s",
		"files":[
			{"path":"profile.json","sha256":"%s","size":%d,"mime":"application/json"},
			{"path":"profile.json","sha256":"%s","size":%d,"mime":"application/json"},
			{"path":"metadata.json","sha256":"%s","size":%d,"mime":"application/json"},
			{"path":"typed-data.json","sha256":"%s","size":%d,"mime":"application/json"}
		]
	}`, hexZeros(), hexZeros(), sum(profile), len(profile), sum(profile), len(profile),
		sum(entries["metadata.json"]), len(entries["metadata.json"]), sum(entries["typed-data.json"]), len(entries["typed-data.json"]))
	if _, err := OpenPackage(rawZip(t, entries)); err == nil {
		t.Fatal("expected error for a duplicate manifest.files path")
	}
}

// TestOpenPackageRejectsMissingMandatoryManifestEntry pins "require the 3
// mandatory JSON files exactly once" — a manifest that never declares
// typed-data.json (even if the archive happens to contain the file) must
// be rejected.
func TestOpenPackageRejectsMissingMandatoryManifestEntry(t *testing.T) {
	entries := validManifestEntries(t)
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	entries["manifest.json"] = fmt.Sprintf(`{
		"packageVersion":"1.0","primaryType":"MintAttestation",
		"profileDigest":"0x%s","metadataDigest":"0x%s",
		"files":[
			{"path":"profile.json","sha256":"%s","size":%d,"mime":"application/json"},
			{"path":"metadata.json","sha256":"%s","size":%d,"mime":"application/json"}
		]
	}`, hexZeros(), hexZeros(), sum(entries["profile.json"]), len(entries["profile.json"]),
		sum(entries["metadata.json"]), len(entries["metadata.json"]))
	if _, err := OpenPackage(rawZip(t, entries)); err == nil {
		t.Fatal("expected error for a manifest missing the mandatory typed-data.json entry")
	}
}

// TestOpenPackageRejectsManifestEntryOutsideProofs pins "restrict other
// entries to declared proofs/ files" — a manifest declaring an extra file
// at the archive root (not proofs/, not one of the 3 mandatory names) must
// be rejected even though it's otherwise well-formed and hash-consistent.
func TestOpenPackageRejectsManifestEntryOutsideProofs(t *testing.T) {
	entries := validManifestEntries(t)
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	extra := "sneaky"
	entries["extra.json"] = extra
	entries["manifest.json"] = fmt.Sprintf(`{
		"packageVersion":"1.0","primaryType":"MintAttestation",
		"profileDigest":"0x%s","metadataDigest":"0x%s",
		"files":[
			{"path":"profile.json","sha256":"%s","size":%d,"mime":"application/json"},
			{"path":"metadata.json","sha256":"%s","size":%d,"mime":"application/json"},
			{"path":"typed-data.json","sha256":"%s","size":%d,"mime":"application/json"},
			{"path":"extra.json","sha256":"%s","size":%d,"mime":"application/json"}
		]
	}`, hexZeros(), hexZeros(), sum(entries["profile.json"]), len(entries["profile.json"]),
		sum(entries["metadata.json"]), len(entries["metadata.json"]),
		sum(entries["typed-data.json"]), len(entries["typed-data.json"]),
		sum(extra), len(extra))
	if _, err := OpenPackage(rawZip(t, entries)); err == nil {
		t.Fatal("expected error for a manifest entry outside proofs/ that isn't a mandatory file")
	}
}

// TestOpenPackageRejectsUnknownManifestField pins strict manifest.json
// decoding (audit: "manifest parsing also uses permissive json.Unmarshal
// without full schema validation" — shared/schemas/rwa-manifest.schema.json
// declares additionalProperties:false).
func TestOpenPackageRejectsUnknownManifestField(t *testing.T) {
	entries := validManifestEntries(t)
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	entries["manifest.json"] = fmt.Sprintf(`{
		"packageVersion":"1.0","primaryType":"MintAttestation",
		"profileDigest":"0x%s","metadataDigest":"0x%s","surprise":true,
		"files":[
			{"path":"profile.json","sha256":"%s","size":%d,"mime":"application/json"},
			{"path":"metadata.json","sha256":"%s","size":%d,"mime":"application/json"},
			{"path":"typed-data.json","sha256":"%s","size":%d,"mime":"application/json"}
		]
	}`, hexZeros(), hexZeros(), sum(entries["profile.json"]), len(entries["profile.json"]),
		sum(entries["metadata.json"]), len(entries["metadata.json"]), sum(entries["typed-data.json"]), len(entries["typed-data.json"]))
	if _, err := OpenPackage(rawZip(t, entries)); err == nil {
		t.Fatal("expected error for an unknown top-level manifest field")
	}
}

// TestOpenPackageAcceptsSelfConsistentManifest is the control: the fixture
// validManifestEntries builds above must be accepted unmodified, so a
// failure in one of the mutation tests above is known to come from the
// specific mutation, not from the shared fixture being invalid.
func TestOpenPackageAcceptsSelfConsistentManifest(t *testing.T) {
	if _, err := OpenPackage(rawZip(t, validManifestEntries(t))); err != nil {
		t.Fatalf("expected the self-consistent fixture to be accepted: %v", err)
	}
}
