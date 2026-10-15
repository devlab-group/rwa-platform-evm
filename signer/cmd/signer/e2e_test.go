package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gokeystore "github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/rwa-platform/signer/internal/attestation"
	"github.com/rwa-platform/signer/internal/canonical"
	rwakeystore "github.com/rwa-platform/signer/internal/keystore"
)

// findGoBinary locates Go through PATH or the test environment's fallback path
func findGoBinary(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	const fallback = "/usr/local/go/bin/go"
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}
	t.Skip("go toolchain not found; skipping end-to-end CLI test")
	return ""
}

// buildSignerBinary compiles cmd/signer into dir and returns its path
func buildSignerBinary(t *testing.T, goBin, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "signer")
	cmd := exec.Command(goBin, "build", "-o", out, ".")
	cmd.Dir = "." // this test file lives in cmd/signer
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("building signer binary: %v\n%s", err, stderr.String())
	}
	return out
}

// docOverrides changes project, unit, timing or auditor values for binding tests.
// The zero value builds the consistent default package.
type docOverrides struct {
	profileProjectID  string // default "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61"
	metadataProjectID string // default: same as profileProjectID
	tokenUnit         string // profile.tokenUnit, default "gram"
	issuanceUnit      string // metadata.issuance.unit, default: same as tokenUnit

	createdAt  time.Time // metadata.createdAt, default: now-1h
	validUntil time.Time // typed-data validUntil; defaults to createdAt+7d within the 30d window

	auditor string // typed-data auditor; defaults to the generated key, or set to a Safe
}

// builtPackage holds the package, signing credentials and values needed for its policy
type builtPackage struct {
	zipBytes      []byte
	keystorePath  string
	password      string
	auditorAddr   string // 0x-prefixed
	profileDigest string // 0x-prefixed
}

// buildGoldenPackage assembles a consistent mint package with computed digests
// and recordKey, plus the keystore and password needed to sign it
func buildGoldenPackage(t *testing.T, chainID, verifyingContract, vault string) builtPackage {
	t.Helper()
	return buildPackageWithOverrides(t, chainID, verifyingContract, vault, docOverrides{})
}

// buildPackageWithOverrides builds a mint package with optional project, unit,
// timing and auditor overrides
func buildPackageWithOverrides(t *testing.T, chainID, verifyingContract, vault string, ov docOverrides) builtPackage {
	t.Helper()

	if ov.profileProjectID == "" {
		ov.profileProjectID = "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61"
	}
	if ov.metadataProjectID == "" {
		ov.metadataProjectID = ov.profileProjectID
	}
	if ov.tokenUnit == "" {
		ov.tokenUnit = "gram"
	}
	if ov.issuanceUnit == "" {
		ov.issuanceUnit = ov.tokenUnit
	}
	if ov.createdAt.IsZero() {
		ov.createdAt = time.Now().UTC().Add(-1 * time.Hour)
	}
	if ov.validUntil.IsZero() {
		ov.validUntil = ov.createdAt.Add(7 * 24 * time.Hour)
	}

	profileJSON := fmt.Sprintf(`{
  "profileVersion": "1.0",
  "projectId": %q,
  "assetType": "allocated-gold-bar",
  "tokenUnit": %q,
  "tokenDecimals": 18,
  "recordIdLabel": "Bar serial number",
  "displayFields": [
    { "label": "Serial", "pointer": "/serialNumber" }
  ],
  "assetSchema": {
    "type": "object",
    "additionalProperties": false,
    "required": ["serialNumber", "weightGrams", "purity"],
    "properties": {
      "serialNumber": { "type": "string", "minLength": 1 },
      "weightGrams": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]+)?$" },
      "purity": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]+)?$" }
    }
  }
}`, ov.profileProjectID, ov.tokenUnit)
	metadataJSON := fmt.Sprintf(`{
  "platformVersion": "1.0",
  "projectId": %q,
  "recordId": "GOLD-BAR-12345",
  "asset": {
    "serialNumber": "12345",
    "weightGrams": "1000",
    "purity": "999.9"
  },
  "issuance": {
    "amount": "1000000000000000000000",
    "unit": %q
  },
  "createdAt": %q
}`, ov.metadataProjectID, ov.issuanceUnit, ov.createdAt.Format(time.RFC3339))

	profileCanon, err := canonical.Canonicalize([]byte(profileJSON))
	if err != nil {
		t.Fatalf("canonicalizing profile: %v", err)
	}
	metadataCanon, err := canonical.Canonicalize([]byte(metadataJSON))
	if err != nil {
		t.Fatalf("canonicalizing metadata: %v", err)
	}
	profileDigest := sha256.Sum256(profileCanon)
	metadataDigest := sha256.Sum256(metadataCanon)
	recordKey := attestation.RecordKey("GOLD-BAR-12345")

	keystorePath, password, auditorAddr := newTestKeystore(t)
	if ov.auditor == "" {
		ov.auditor = auditorAddr.Hex()
	}

	typedData := map[string]any{
		"primaryType": "MintAttestation",
		"domain": map[string]any{
			"name":              attestation.DomainName,
			"version":           attestation.DomainVersion,
			"chainId":           json.Number(chainID),
			"verifyingContract": verifyingContract,
		},
		"message": map[string]any{
			"auditor":        ov.auditor,
			"profileDigest":  "0x" + hex.EncodeToString(profileDigest[:]),
			"recordId":       "GOLD-BAR-12345",
			"recordKey":      "0x" + hex.EncodeToString(recordKey[:]),
			"metadataDigest": "0x" + hex.EncodeToString(metadataDigest[:]),
			"amount":         "1000000000000000000000",
			"nonce":          "42",
			"validUntil":     ov.validUntil.Unix(),
			"vault":          vault,
		},
	}
	typedDataJSON, err := json.Marshal(typedData)
	if err != nil {
		t.Fatalf("marshaling typed-data.json: %v", err)
	}

	fileEntry := func(name string, data []byte, mime string) map[string]any {
		sum := sha256.Sum256(data)
		return map[string]any{
			"path":   name,
			"sha256": hex.EncodeToString(sum[:]),
			"size":   len(data),
			"mime":   mime,
		}
	}
	manifest := map[string]any{
		"packageVersion": "1.0",
		"primaryType":    "MintAttestation",
		"profileDigest":  "0x" + hex.EncodeToString(profileDigest[:]),
		"metadataDigest": "0x" + hex.EncodeToString(metadataDigest[:]),
		"files": []map[string]any{
			fileEntry("profile.json", profileCanon, "application/json"),
			fileEntry("metadata.json", metadataCanon, "application/json"),
			fileEntry("typed-data.json", typedDataJSON, "application/json"),
		},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshaling manifest.json: %v", err)
	}

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for name, data := range map[string][]byte{
		"manifest.json":   manifestJSON,
		"profile.json":    profileCanon,
		"metadata.json":   metadataCanon,
		"typed-data.json": typedDataJSON,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zw.Create(%q): %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("writing %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zw.Close: %v", err)
	}

	return builtPackage{
		zipBytes:      zipBuf.Bytes(),
		keystorePath:  keystorePath,
		password:      password,
		auditorAddr:   auditorAddr.Hex(),
		profileDigest: "0x" + hex.EncodeToString(profileDigest[:]),
	}
}

// newTestKeystore returns a fresh V3 keystore's path, password and key address
func newTestKeystore(t *testing.T) (string, string, common.Address) {
	t.Helper()
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	// Use the signer's KDF floor; light scrypt parameters are rejected before derivation
	ks := gokeystore.NewKeyStore(t.TempDir(), rwakeystore.MinScryptN, rwakeystore.MinScryptP)
	const password = "e2e-test-password"
	account, err := ks.ImportECDSA(priv, password)
	if err != nil {
		t.Fatalf("ImportECDSA: %v", err)
	}
	return account.URL.Path, password, account.Address
}

// writePolicyFile pins the fixture's deployment values and the caller's project ID
func writePolicyFile(t *testing.T, dir, chainID, controller, vault, projectID string, pkg builtPackage) string {
	t.Helper()
	return writePolicyFileWithOverrides(t, dir, chainID, controller, vault, pkg.auditorAddr, projectID, pkg.profileDigest, "")
}

// writePolicyFileWithOverrides permits deployment mismatches or a custom lifetime
func writePolicyFileWithOverrides(t *testing.T, dir, chainID, controller, vault, auditor, projectID, profileDigest, maxLifetimeHours string) string {
	t.Helper()
	pol := map[string]any{
		"chainId":       chainID,
		"controller":    controller,
		"vault":         vault,
		"auditor":       auditor,
		"projectId":     projectID,
		"profileDigest": profileDigest,
	}
	if maxLifetimeHours != "" {
		pol["maxAttestationLifetimeHours"] = json.Number(maxLifetimeHours)
	}
	raw, err := json.Marshal(pol)
	if err != nil {
		t.Fatalf("marshaling policy file: %v", err)
	}
	path := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("writing policy file: %v", err)
	}
	return path
}

const (
	testChainID    = "31337"
	testController = "0x5FbDB2315678afecb367f032d93F642f64180aa3"
	testVault      = "0xe7f1725E7734CE288F8367e1Bb143E90bb3F0512"
	testProjectID  = "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61"
)

// Sign a consistent package with the built CLI and verify signed-result.json and its
// signature
func TestE2E_SignGoldenPackage(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("signer sign failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	resultPath := filepath.Join(workDir, "signed-result.json")
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("reading signed-result.json: %v", err)
	}
	var result struct {
		FormatVersion   string `json:"formatVersion"`
		Auditor         string `json:"auditor"`
		PrimaryType     string `json:"primaryType"`
		TypedDataDigest string `json:"typedDataDigest"`
		Signature       string `json:"signature"`
		SignedAt        string `json:"signedAt"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshaling signed-result.json: %v", err)
	}

	if result.FormatVersion != "1.0" {
		t.Errorf("formatVersion = %q", result.FormatVersion)
	}
	if result.PrimaryType != "MintAttestation" {
		t.Errorf("primaryType = %q", result.PrimaryType)
	}

	sigBytes, err := hex.DecodeString(strings.TrimPrefix(result.Signature, "0x"))
	if err != nil || len(sigBytes) != 65 {
		t.Fatalf("signature is not a valid 65-byte hex string: %q (err=%v)", result.Signature, err)
	}
	digestBytes, err := hex.DecodeString(strings.TrimPrefix(result.TypedDataDigest, "0x"))
	if err != nil || len(digestBytes) != 32 {
		t.Fatalf("typedDataDigest is not a valid 32-byte hex string: %q (err=%v)", result.TypedDataDigest, err)
	}
	var digest [32]byte
	copy(digest[:], digestBytes)

	recovered, err := attestation.Recover(digest, sigBytes)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !strings.EqualFold(recovered.Hex(), result.Auditor) {
		t.Errorf("recovered signer %s does not match declared auditor %s", recovered.Hex(), result.Auditor)
	}
}

// Refuse an archived amount changed without updating manifest hashes
func TestE2E_RejectsTamperedAmount(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)

	// Change the typed-data amount while leaving the manifest hashes stale
	zr, err := zip.NewReader(bytes.NewReader(pkg.zipBytes), int64(len(pkg.zipBytes)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	tampered := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %q: %v", f.Name, err)
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("reading %q: %v", f.Name, err)
		}
		rc.Close()
		tampered[f.Name] = buf.Bytes()
	}
	tampered["typed-data.json"] = bytes.ReplaceAll(tampered["typed-data.json"],
		[]byte("1000000000000000000000"), []byte("9999999999999999999999"))

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for name, data := range tampered {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zw.Create: %v", err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zw.Close: %v", err)
	}

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, zipBuf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to reject tampered package, but it succeeded\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "does not match") {
		t.Errorf("expected a mismatch error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when validation fails")
	}
}

// Reject mismatched profile and metadata project IDs
func TestE2E_RejectsCrossProjectPackage(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildPackageWithOverrides(t, testChainID, testController, testVault,
		docOverrides{
			profileProjectID:  testProjectID,
			metadataProjectID: "00000000-0000-0000-0000-000000000000",
		})

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to reject a cross-project package, but it succeeded\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "projectId") {
		t.Errorf("expected a projectId mismatch error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when validation fails")
	}
}

// Require metadata issuance.unit to match profile tokenUnit
func TestE2E_RejectsWrongIssuanceUnit(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildPackageWithOverrides(t, testChainID, testController, testVault,
		docOverrides{tokenUnit: "gram", issuanceUnit: "troy-ounce"})

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to reject a mismatched issuance unit, but it succeeded\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unit") {
		t.Errorf("expected a unit mismatch error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when validation fails")
	}
}

// Reject password files accessible to other accounts
func TestE2E_RejectsWorldReadablePasswordFile(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	pwPath := filepath.Join(workDir, "password.txt")
	if err := os.WriteFile(pwPath, []byte(pkg.password+"\n"), 0o644); err != nil {
		t.Fatalf("writing password file: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode", "--password-file", pwPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to reject a world-readable --password-file, but it succeeded\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "readable") {
		t.Errorf("expected a group/world-readable password-file error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when the password file is rejected")
	}
}

// Reject V3 parameters below the KDF floor before derivation
func TestE2E_RejectsWeakKDFParams(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)

	// Re-encrypt the same key with light scrypt N=2^12, below MinScryptN=2^17
	weakDir := t.TempDir()
	keyJSON, err := os.ReadFile(pkg.keystorePath)
	if err != nil {
		t.Fatalf("reading golden keystore: %v", err)
	}
	key, err := gokeystore.DecryptKey(keyJSON, pkg.password)
	if err != nil {
		t.Fatalf("decrypting golden keystore: %v", err)
	}
	ks := gokeystore.NewKeyStore(weakDir, gokeystore.LightScryptN, gokeystore.LightScryptP)
	account, err := ks.ImportECDSA(key.PrivateKey, pkg.password)
	if err != nil {
		t.Fatalf("ImportECDSA (weak params): %v", err)
	}

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", account.URL.Path, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to reject a weak-KDF-parameter keystore, but it succeeded\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "weaker than the minimum policy") {
		t.Errorf("expected a KDF-strength policy error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when the keystore's KDF parameters are rejected")
	}
}

// Auto-detect rwa-argon2id-v1 through --keystore
func TestE2E_SignsWithNativeKeystoreFormat(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)

	keyJSON, err := os.ReadFile(pkg.keystorePath)
	if err != nil {
		t.Fatalf("reading golden keystore: %v", err)
	}
	key, err := gokeystore.DecryptKey(keyJSON, pkg.password)
	if err != nil {
		t.Fatalf("decrypting golden keystore: %v", err)
	}
	nativeJSON, err := rwakeystore.CreateNative(key.PrivateKey, "a-strong-argon2id-password")
	if err != nil {
		t.Fatalf("CreateNative: %v", err)
	}
	workDir := t.TempDir()
	nativePath := filepath.Join(workDir, "auditor.native.json")
	if err := os.WriteFile(nativePath, nativeJSON, 0o600); err != nil {
		t.Fatalf("writing native keystore: %v", err)
	}

	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", nativePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader("a-strong-argon2id-password\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("signer sign (native keystore) failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err != nil {
		t.Errorf("expected signed-result.json: %v", err)
	}
}

// Keep raw private-key hex out of stdout, stderr and signed-result.json
func TestE2E_NeverExportsPrivateKey(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)

	keyJSON, err := os.ReadFile(pkg.keystorePath)
	if err != nil {
		t.Fatalf("reading golden keystore: %v", err)
	}
	key, err := gokeystore.DecryptKey(keyJSON, pkg.password)
	if err != nil {
		t.Fatalf("decrypting golden keystore: %v", err)
	}
	privHex := hex.EncodeToString(crypto.FromECDSA(key.PrivateKey))

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("signer sign failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	resultData, err := os.ReadFile(filepath.Join(workDir, "signed-result.json"))
	if err != nil {
		t.Fatalf("reading signed-result.json: %v", err)
	}

	if strings.Contains(stdout.String(), privHex) {
		t.Error("private key hex found in stdout")
	}
	if strings.Contains(stderr.String(), privHex) {
		t.Error("private key hex found in stderr")
	}
	if strings.Contains(string(resultData), privHex) {
		t.Error("private key hex found in signed-result.json")
	}
}

// Verify audit log and anchor after signing, including unlock_success and signed events
func TestE2E_SignWritesVerifiableAuditTrail(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)
	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("signer sign failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	if err := rwakeystore.VerifyAuditLogAnchored(pkg.keystorePath, rwakeystore.DefaultAnchorPath(pkg.keystorePath)); err != nil {
		t.Errorf("expected the audit trail written by a real sign to verify: %v", err)
	}
	logData, err := os.ReadFile(pkg.keystorePath + ".auditlog.jsonl")
	if err != nil {
		t.Fatalf("reading audit log: %v", err)
	}
	for _, want := range []string{`"event":"unlock_success"`, `"event":"signed"`} {
		if !strings.Contains(string(logData), want) {
			t.Errorf("audit log missing %s:\n%s", want, logData)
		}
	}
}

// Refuse signing with a broken audit chain
func TestE2E_RefusesToSignWhenAuditLogIsCorrupted(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)
	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	corrupted := []byte(`{"seq":1,"time":"2020-01-01T00:00:00Z","event":"unlock_success","prevHash":"","hash":"0000000000000000000000000000000000000000000000000000000000000000"}` + "\n")
	if err := os.WriteFile(pkg.keystorePath+".auditlog.jsonl", corrupted, 0o600); err != nil {
		t.Fatalf("writing pre-corrupted audit log: %v", err)
	}

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to refuse to sign against a corrupted audit log\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "audit log integrity") {
		t.Errorf("unexpected error, stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when the audit log is corrupted")
	}
}

// Normal signing must fail when audit writes cannot complete
func TestE2E_ProductionFailsClosedWhenAuditLogCannotBeWritten(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not work the same way on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores permission bits, so this can't be exercised here")
	}
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)
	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	keystoreDir := filepath.Dir(pkg.keystorePath)
	if err := os.Chmod(keystoreDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(keystoreDir, 0o700) }) // let t.TempDir() clean up afterward

	// Confirm through stdin to exercise normal audit-write enforcement
	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath)
	cmd.Stdin = strings.NewReader("y\n" + pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to fail closed when it cannot durably record the audit trail\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "audit") {
		t.Errorf("expected an audit-trail-related error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when the audit trail cannot be recorded (production/fail-closed)")
	}
}

// Require --policy even for a valid package
func TestE2E_RequiresPolicyFlag(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to refuse to run without --policy, but it succeeded\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--policy is required") {
		t.Errorf("expected a --policy-required error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when validation fails")
	}
}

// Require --unsafe-test-mode to skip confirmation with --yes
func TestE2E_RejectsYesWithoutUnsafeTestMode(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to reject --yes without --unsafe-test-mode, but it succeeded\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unsafe-test-mode") {
		t.Errorf("expected an --unsafe-test-mode error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when validation fails")
	}
}

// Reject an internally consistent package for a project outside policy
func TestE2E_RejectsPolicyProjectIDMismatch(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, "11111111-1111-1111-1111-111111111111", pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to reject a policy projectId mismatch, but it succeeded\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "policy projectId") {
		t.Errorf("expected a policy projectId mismatch error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when validation fails")
	}
}

// Require policy controller to match domain.verifyingContract
func TestE2E_RejectsPolicyControllerMismatch(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	pkg := buildGoldenPackage(t, testChainID, testController, testVault)

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, "0x000000000000000000000000000000000000dd", testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to reject a policy controller mismatch, but it succeeded\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "policy controller") {
		t.Errorf("expected a policy controller mismatch error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when validation fails")
	}
}

// Reject an expired validUntil
func TestE2E_RejectsExpiredValidUntil(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	createdAt := time.Now().UTC().Add(-48 * time.Hour)
	pkg := buildPackageWithOverrides(t, testChainID, testController, testVault, docOverrides{
		createdAt:  createdAt,
		validUntil: createdAt.Add(1 * time.Hour), // in the past relative to "now"
	})

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to reject an expired validUntil, but it succeeded\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "validUntil") {
		t.Errorf("expected a validUntil error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when validation fails")
	}
}

// Reject validUntil beyond the policy lifetime
func TestE2E_RejectsFarFutureValidUntil(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	createdAt := time.Now().UTC().Add(-1 * time.Hour)
	pkg := buildPackageWithOverrides(t, testChainID, testController, testVault, docOverrides{
		createdAt:  createdAt,
		validUntil: createdAt.Add(60 * 24 * time.Hour), // 60 days: exceeds the 30-day default policy horizon
	})

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected signer to reject a far-future validUntil, but it succeeded\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "exceeds the maximum attestation lifetime") {
		t.Errorf("expected a max-lifetime error, got stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when validation fails")
	}
}

// Accept a longer policy window exceeding the default 30 days
func TestE2E_AcceptsExtendedLifetimePolicy(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	createdAt := time.Now().UTC().Add(-1 * time.Hour)
	pkg := buildPackageWithOverrides(t, testChainID, testController, testVault, docOverrides{
		createdAt:  createdAt,
		validUntil: createdAt.Add(60 * 24 * time.Hour), // 60 days: fine under a 90-day policy
	})

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	policyPath := writePolicyFileWithOverrides(t, workDir, testChainID, testController, testVault, pkg.auditorAddr, testProjectID, pkg.profileDigest, "2160" /* 90 days */)

	cmd := exec.Command(signerBin, "sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath, "--yes", "--unsafe-test-mode")
	cmd.Stdin = strings.NewReader(pkg.password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("signer sign failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "signed-result.json")); err != nil {
		t.Errorf("expected signed-result.json to be written: %v", err)
	}
}

// Decrypt a newly created keystore and verify creation does not print its private key
func TestE2E_KeystoreCreate_WritesUsableKeystore(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	workDir := t.TempDir()
	outPath := filepath.Join(workDir, "auditor.json")

	cmd := exec.Command(signerBin, "keystore", "create", "--out", outPath)
	cmd.Stdin = strings.NewReader("a-strong-new-keystore-pw\na-strong-new-keystore-pw\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("keystore create failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	priv, addr, err := rwakeystore.Load(outPath, "a-strong-new-keystore-pw")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer rwakeystore.Zero(priv)

	if !strings.Contains(stdout.String(), addr.Hex()) {
		t.Errorf("expected stdout to report the created address %s, got:\n%s", addr.Hex(), stdout.String())
	}
	privHex := hex.EncodeToString(crypto.FromECDSA(priv))
	if strings.Contains(stdout.String(), privHex) || strings.Contains(stderr.String(), privHex) {
		t.Error("keystore create must never print the private key")
	}
}

func TestE2E_KeystoreCreate_RejectsWeakPassword(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	outPath := filepath.Join(t.TempDir(), "auditor.json")
	cmd := exec.Command(signerBin, "keystore", "create", "--out", outPath)
	cmd.Stdin = strings.NewReader("short\nshort\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected keystore create to reject a too-short password")
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Error("keystore file must not be written when the password is rejected")
	}
}

func TestE2E_KeystoreCreate_RejectsMismatchedConfirmation(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	outPath := filepath.Join(t.TempDir(), "auditor.json")
	cmd := exec.Command(signerBin, "keystore", "create", "--out", outPath)
	cmd.Stdin = strings.NewReader("a-strong-new-keystore-pw\na-different-password-9\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected keystore create to reject a mismatched password confirmation")
	}
	if !strings.Contains(stderr.String(), "match") {
		t.Errorf("unexpected error, stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Error("keystore file must not be written when confirmation does not match")
	}
}

func TestE2E_KeystoreCreate_RefusesToOverwrite(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	outPath := filepath.Join(t.TempDir(), "auditor.json")
	if err := os.WriteFile(outPath, []byte("not a keystore"), 0o600); err != nil {
		t.Fatalf("writing pre-existing file: %v", err)
	}

	cmd := exec.Command(signerBin, "keystore", "create", "--out", outPath)
	cmd.Stdin = strings.NewReader("a-strong-new-keystore-pw\na-strong-new-keystore-pw\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected keystore create to refuse to overwrite an existing file")
	}
	data, err := os.ReadFile(outPath)
	if err != nil || string(data) != "not a keystore" {
		t.Error("existing file must be left untouched when creation is refused")
	}
}

// Exclusive creation must reject a dangling --out symlink without writing its target
func TestE2E_KeystoreCreate_RefusesToFollowDanglingSymlink(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	workDir := t.TempDir()
	target := filepath.Join(workDir, "elsewhere.json") // does not exist
	linkPath := filepath.Join(workDir, "auditor.json")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	cmd := exec.Command(signerBin, "keystore", "create", "--out", linkPath)
	cmd.Stdin = strings.NewReader("a-strong-new-keystore-pw\na-strong-new-keystore-pw\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected keystore create to refuse a dangling symlink at --out\nstdout:\n%s", stdout.String())
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("keystore create must not have written through the symlink to its target")
	}
	if _, err := os.Lstat(linkPath); err != nil {
		t.Errorf("the symlink itself should be left in place, untouched: %v", err)
	}
}

// Importing an owner-only hex key file preserves its key and address after decryption
func TestE2E_KeystoreImport_RoundTrip(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	wantAddr := crypto.PubkeyToAddress(priv.PublicKey)

	workDir := t.TempDir()
	privkeyPath := filepath.Join(workDir, "privkey.hex")
	if err := os.WriteFile(privkeyPath, []byte(hex.EncodeToString(crypto.FromECDSA(priv))+"\n"), 0o600); err != nil {
		t.Fatalf("writing privkey file: %v", err)
	}
	outPath := filepath.Join(workDir, "auditor.json")

	cmd := exec.Command(signerBin, "keystore", "import", "--privkey-file", privkeyPath, "--out", outPath)
	cmd.Stdin = strings.NewReader("a-strong-imported-pw-123\na-strong-imported-pw-123\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("keystore import failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	gotPriv, gotAddr, err := rwakeystore.Load(outPath, "a-strong-imported-pw-123")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer rwakeystore.Zero(gotPriv)
	if gotAddr != wantAddr {
		t.Errorf("address = %s, want %s", gotAddr.Hex(), wantAddr.Hex())
	}
	if gotPriv.D.Cmp(priv.D) != 0 {
		t.Error("imported key does not round-trip")
	}
}

func TestE2E_KeystoreImport_RejectsWorldReadablePrivkeyFile(t *testing.T) {
	goBin := findGoBinary(t)
	binDir := t.TempDir()
	signerBin := buildSignerBinary(t, goBin, binDir)

	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	workDir := t.TempDir()
	privkeyPath := filepath.Join(workDir, "privkey.hex")
	if err := os.WriteFile(privkeyPath, []byte(hex.EncodeToString(crypto.FromECDSA(priv))+"\n"), 0o644); err != nil {
		t.Fatalf("writing world-readable privkey file: %v", err)
	}
	outPath := filepath.Join(workDir, "auditor.json")

	cmd := exec.Command(signerBin, "keystore", "import", "--privkey-file", privkeyPath, "--out", outPath)
	cmd.Stdin = strings.NewReader("a-strong-imported-pw-123\na-strong-imported-pw-123\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected keystore import to reject a world-readable --privkey-file")
	}
	if !strings.Contains(stderr.String(), "readable") {
		t.Errorf("unexpected error, stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Error("keystore file must not be written when --privkey-file is rejected")
	}
}
