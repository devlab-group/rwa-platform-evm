package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/rwa-platform/signer/internal/attestation"
)

// Safe address from the shared vector; these tests make no RPC calls
const testSafe = "0x9a7F3dfDEeBb51eAbBbA3c8eCCf8cF2a983A1C72"

func runSigner(t *testing.T, bin, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func writeSafePolicy(t *testing.T, dir string, pkg builtPackage, owners []string, threshold int) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"chainId":       testChainID,
		"controller":    testController,
		"vault":         testVault,
		"auditor":       testSafe,
		"projectId":     testProjectID,
		"profileDigest": pkg.profileDigest,
		"safe":          map[string]any{"owners": owners, "threshold": threshold},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestE2E_SafeAuditor_SignAndCombine exercises two-owner signing and combining through the CLI,
// including non-owner, threshold and duplicate-share refusals and signature ordering
func TestE2E_SafeAuditor_SignAndCombine(t *testing.T) {
	signerBin := buildSignerBinary(t, findGoBinary(t), t.TempDir())
	pkg := buildPackageWithOverrides(t, testChainID, testController, testVault, docOverrides{auditor: testSafe})
	ownerBPath, ownerBPassword, ownerB := newTestKeystore(t)
	outsiderPath, outsiderPassword, _ := newTestKeystore(t)
	_, _, ownerC := newTestKeystore(t)

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	policyPath := writeSafePolicy(t, workDir, pkg, []string{pkg.auditorAddr, ownerB.Hex(), ownerC.Hex()}, 2)

	sign := func(keystorePath, password, out string) (string, error) {
		return runSigner(t, signerBin, password+"\n", "sign", pkgPath, "--keystore", keystorePath, "--policy", policyPath, "--out", out, "--yes", "--unsafe-test-mode")
	}
	shareA, shareB := filepath.Join(workDir, "share-a.json"), filepath.Join(workDir, "share-b.json")
	if out, err := sign(pkg.keystorePath, pkg.password, shareA); err != nil {
		t.Fatalf("owner A sign: %v\n%s", err, out)
	}
	if out, err := sign(ownerBPath, ownerBPassword, shareB); err != nil {
		t.Fatalf("owner B sign: %v\n%s", err, out)
	}
	if out, err := sign(outsiderPath, outsiderPassword, filepath.Join(workDir, "share-x.json")); err == nil || !strings.Contains(out, "is not an owner") {
		t.Fatalf("non-owner sign: err=%v, want a refusal\n%s", err, out)
	}

	combine := func(shares ...string) (string, error) {
		return runSigner(t, signerBin, "", append([]string{"combine", "--policy", policyPath}, shares...)...)
	}
	if out, err := combine(shareA); err == nil || !strings.Contains(out, "needs 2") {
		t.Fatalf("one share: err=%v, want a threshold error\n%s", err, out)
	}
	if out, err := combine(shareA, shareA); err == nil || !strings.Contains(out, "second share") {
		t.Fatalf("duplicate share: err=%v, want a duplicate error\n%s", err, out)
	}
	if out, err := combine(shareA, shareB); err != nil {
		t.Fatalf("combine: %v\n%s", err, out)
	}

	var result struct {
		Auditor         string `json:"auditor"`
		TypedDataDigest string `json:"typedDataDigest"`
		Signature       string `json:"signature"`
	}
	data, err := os.ReadFile(filepath.Join(workDir, "signed-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Auditor != common.HexToAddress(testSafe).Hex() {
		t.Errorf("auditor = %s, want the Safe %s", result.Auditor, testSafe)
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(result.Signature, "0x"))
	if err != nil || len(sig) != 130 {
		t.Fatalf("signature is not two 65-byte signatures: %q", result.Signature)
	}
	safeHash, err := attestation.SafeMessageHash(big.NewInt(31337), common.HexToAddress(testSafe), common.HexToHash(result.TypedDataDigest))
	if err != nil {
		t.Fatal(err)
	}
	first, err1 := attestation.Recover(safeHash, sig[:65])
	second, err2 := attestation.Recover(safeHash, sig[65:])
	if err1 != nil || err2 != nil {
		t.Fatalf("recover: %v, %v", err1, err2)
	}
	owners := map[common.Address]bool{common.HexToAddress(pkg.auditorAddr): true, ownerB: true}
	if !owners[first] || !owners[second] || first == second {
		t.Fatalf("signatures recover to %s and %s, want owners A and B", first.Hex(), second.Hex())
	}
	if bytes.Compare(first[:], second[:]) >= 0 {
		t.Errorf("signatures are not in ascending owner order: %s then %s", first.Hex(), second.Hex())
	}
}
