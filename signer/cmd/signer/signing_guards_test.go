package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/rwa-platform/signer/internal/attestation"
)

const (
	otherAddr   = "0x70997970C51812dc3A010C7d01b50e0d17dc79C8"
	otherDigest = "0x1111111111111111111111111111111111111111111111111111111111111111"
)

func assertNoResult(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "signed-result.json")); err == nil {
		t.Error("signed-result.json must not be written when signing is refused")
	}
}

// Policy mismatches and a non-auditor signing key must each fail with the relevant field named
func TestE2E_PolicyBindingsAreEnforcedIndividually(t *testing.T) {
	for _, burn := range []bool{false, true} {
		name := "mint"
		if burn {
			name = "burn"
		}
		t.Run(name, func(t *testing.T) { policyBindings(t, burn) })
	}
}

func policyBindings(t *testing.T, burn bool) {
	pkg := buildPackageWithOverrides(t, testChainID, testController, testVault, docOverrides{burn: burn})
	wrongKeystore, wrongPassword, wrongAddr := newTestKeystore(t)

	tests := []struct {
		name    string
		policy  func(dir string) string
		pkg     builtPackage
		wantErr string
	}{
		{
			name: "profileDigest pin",
			policy: func(dir string) string {
				return writePolicyFileWithOverrides(t, dir, testChainID, testController, testVault, pkg.auditorAddr, testProjectID, otherDigest, "")
			},
			pkg:     pkg,
			wantErr: "recomputed profileDigest " + pkg.profileDigest + " does not match policy profileDigest " + otherDigest,
		},
		{
			name: "chainId",
			policy: func(dir string) string {
				return writePolicyFileWithOverrides(t, dir, "1", testController, testVault, pkg.auditorAddr, testProjectID, pkg.profileDigest, "")
			},
			pkg:     pkg,
			wantErr: "typed-data chainId 31337 does not match policy chainId 1",
		},
		{
			name: "controller",
			policy: func(dir string) string {
				return writePolicyFileWithOverrides(t, dir, testChainID, otherAddr, testVault, pkg.auditorAddr, testProjectID, pkg.profileDigest, "")
			},
			pkg:     pkg,
			wantErr: "typed-data verifyingContract " + testController + " does not match policy controller " + otherAddr,
		},
		{
			name: "vault",
			policy: func(dir string) string {
				return writePolicyFileWithOverrides(t, dir, testChainID, testController, otherAddr, pkg.auditorAddr, testProjectID, pkg.profileDigest, "")
			},
			pkg:     pkg,
			wantErr: "typed-data vault " + testVault + " does not match policy vault " + otherAddr,
		},
		{
			name: "auditor",
			policy: func(dir string) string {
				return writePolicyFileWithOverrides(t, dir, testChainID, testController, testVault, otherAddr, testProjectID, pkg.profileDigest, "")
			},
			pkg:     pkg,
			wantErr: "typed-data auditor " + pkg.auditorAddr + " does not match policy auditor " + otherAddr,
		},
		{
			name: "signing key is not the declared auditor",
			policy: func(dir string) string {
				return writePolicyFile(t, dir, testChainID, testController, testVault, testProjectID, pkg)
			},
			pkg: builtPackage{
				zipBytes: pkg.zipBytes, keystorePath: wrongKeystore, password: wrongPassword,
				auditorAddr: pkg.auditorAddr, profileDigest: pkg.profileDigest,
			},
			wantErr: "signing key address " + wrongAddr.Hex() + " does not match typed-data auditor " + pkg.auditorAddr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			out := signPackage(t, tt.pkg, dir, tt.policy(dir), tt.pkg.password+"\n", "--yes", "--unsafe-test-mode")
			if out.err == nil {
				t.Fatalf("sign succeeded, want a refusal\nstdout:\n%s", out.stdout)
			}
			if !strings.Contains(out.stderr, tt.wantErr) {
				t.Errorf("stderr = %q\nwant it to contain %q", out.stderr, tt.wantErr)
			}
			assertNoResult(t, dir)
		})
	}
}

// Answers other than y or yes abort the run before any key is unlocked
func TestE2E_DecliningConfirmationAborts(t *testing.T) {
	for name, answer := range map[string]string{
		"n": "n\n", "no": "no\n", "blank": "\n", "end of input": "", "near miss": "yeah\n",
	} {
		t.Run(name, func(t *testing.T) {
			pkg := buildGoldenPackage(t, testChainID, testController, testVault)
			dir := t.TempDir()
			policyPath := writePolicyFile(t, dir, testChainID, testController, testVault, testProjectID, pkg)
			out := signPackage(t, pkg, dir, policyPath, answer)
			if out.err == nil {
				t.Fatalf("sign succeeded after answer %q\nstdout:\n%s", answer, out.stdout)
			}
			if !strings.Contains(out.stderr, "aborted: not confirmed") {
				t.Errorf("stderr = %q, want %q", out.stderr, "aborted: not confirmed")
			}
			if !strings.Contains(out.stdout, "Sign this attestation [y/N]: ") {
				t.Errorf("stdout lacks the confirmation prompt:\n%s", out.stdout)
			}
			assertNoResult(t, dir)
			if _, err := os.Stat(pkg.keystorePath + ".auditlog.jsonl"); err == nil {
				t.Error("declining must not unlock the key, but an audit log was written")
			}
		})
	}
}

// reviewValue returns the value printed for label on the review screen
func reviewValue(t *testing.T, screen, label string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^[ !] ` + regexp.QuoteMeta(label) + ` *: (.*)$`)
	m := re.FindStringSubmatch(screen)
	if m == nil {
		t.Fatalf("review screen has no %q line:\n%s", label, screen)
	}
	return m[1]
}

// The review screen shows every value the auditor is about to sign, exactly as packaged,
// then "y" signs
func TestE2E_ReviewScreenShowsSignedValues(t *testing.T) {
	for _, primary := range []string{"MintAttestation", "BurnAttestation"} {
		t.Run(primary, func(t *testing.T) {
			pkg := buildPackageWithOverrides(t, testChainID, testController, testVault,
				docOverrides{burn: primary == "BurnAttestation", nonce: "7"})
			dir := t.TempDir()
			policyPath := writePolicyFile(t, dir, testChainID, testController, testVault, testProjectID, pkg)
			pwFile := filepath.Join(dir, "pw")
			if err := os.WriteFile(pwFile, []byte(pkg.password+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			out := signPackage(t, pkg, dir, policyPath, "y\n", "--password-file", pwFile)
			if out.err != nil {
				t.Fatalf("sign: %v\nstderr:\n%s", out.err, out.stderr)
			}

			var td struct {
				Domain  fieldMap `json:"domain"`
				Message fieldMap `json:"message"`
			}
			dec := json.NewDecoder(bytes.NewReader(pkg.typedDataJSON))
			dec.UseNumber()
			if err := dec.Decode(&td); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{
				"primaryType":                          primary,
				"chainId":                              fmt.Sprint(td.Domain["chainId"]),
				"verifyingContract (SupplyController)": testController,
				"auditor":                              pkg.auditorAddr,
				"vault":                                testVault,
				"profileDigest":                        fmt.Sprint(td.Message["profileDigest"]),
				"metadataDigest":                       fmt.Sprint(td.Message["metadataDigest"]),
				"amount (raw, smallest unit)":          "1000000000000000000000",
				"amount (normalized)":                  "1000.000000000000000000 gram",
				"nonce":                                "7",
				"EIP-712 digest":                       oracleDigest(t, primary, td.Domain, td.Message).Hex(),
			}
			if primary == "BurnAttestation" {
				want["operationId"] = fmt.Sprint(td.Message["operationId"])
			} else {
				want["recordId"] = "GOLD-BAR-12345"
				want["recordKey"] = fmt.Sprint(td.Message["recordKey"])
			}
			for label, value := range want {
				if got := reviewValue(t, out.stdout, label); got != value {
					t.Errorf("review %q = %q, want %q", label, got, value)
				}
			}
			banner := "MINT - THIS WILL INCREASE TOKEN SUPPLY"
			if primary == "BurnAttestation" {
				banner = "BURN - THIS WILL DECREASE TOKEN SUPPLY"
			}
			if !strings.Contains(out.stdout, banner) {
				t.Errorf("review screen lacks banner %q", banner)
			}
			_, _, digest, _ := readSignedResult(t, filepath.Join(dir, "signed-result.json"))
			if digest != want["EIP-712 digest"] {
				t.Errorf("signed digest %s differs from the digest shown for review %s", digest, want["EIP-712 digest"])
			}
		})
	}
}

// Safe shares that do not verify are refused by combine, each for its own reason
func TestE2E_CombineRejectsBadShares(t *testing.T) {
	bin := buildSignerBinary(t, findGoBinary(t), "")
	pkg := buildPackageWithOverrides(t, testChainID, testController, testVault, docOverrides{auditor: testSafe})
	ownerBPath, ownerBPassword, ownerB := newTestKeystore(t)
	outsiderPath, outsiderPassword, outsider := newTestKeystore(t)

	workDir := t.TempDir()
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	policyPath := writeSafePolicy(t, workDir, pkg, []string{pkg.auditorAddr, ownerB.Hex()}, 2)
	// The outsider signs against a policy that lists it, then combine runs against the real one
	outsiderPolicy := filepath.Join(workDir, "outsider")
	if err := os.Mkdir(outsiderPolicy, 0o755); err != nil {
		t.Fatal(err)
	}
	outsiderPolicyPath := writeSafePolicy(t, outsiderPolicy, pkg, []string{pkg.auditorAddr, outsider.Hex()}, 2)

	sign := func(keystorePath, password, policy, out string) {
		t.Helper()
		if o, err := runSigner(t, bin, password+"\n", "sign", pkgPath, "--keystore", keystorePath, "--policy", policy, "--out", out, "--yes", "--unsafe-test-mode"); err != nil {
			t.Fatalf("sign: %v\n%s", err, o)
		}
	}
	shareA, shareB, shareX := filepath.Join(workDir, "a.json"), filepath.Join(workDir, "b.json"), filepath.Join(workDir, "x.json")
	sign(pkg.keystorePath, pkg.password, policyPath, shareA)
	sign(ownerBPath, ownerBPassword, policyPath, shareB)
	sign(outsiderPath, outsiderPassword, outsiderPolicyPath, shareX)

	readShare := func(path string) map[string]any {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	flipped := "0x" + strings.Repeat("ab", 32)
	otherSafe := common.HexToAddress(otherAddr).Hex()
	tests := []struct {
		name    string
		edit    func(a, b map[string]any) // edits owner B's share; shareX replaces it when useX is set
		useX    bool
		wantErr string
	}{
		{"share signed by one owner but labelled as another", func(_, b map[string]any) { b["owner"] = pkg.auditorAddr }, false, "signature recovers to " + ownerB.Hex() + ", not the share's owner " + pkg.auditorAddr},
		{"wrong chainId", func(_, b map[string]any) { b["chainId"] = "1" }, false, "share is for Safe " + testSafe + " on chain 1, but the policy names Safe " + testSafe + " on chain 31337"},
		{"wrong Safe address", func(_, b map[string]any) { b["safe"] = otherSafe }, false, "share is for Safe " + otherSafe + " on chain 31337, but the policy names Safe " + testSafe},
		{"shares with different digests", func(_, b map[string]any) { b["typedDataDigest"] = flipped }, false, "share signs MintAttestation " + flipped},
		{"tampered safeMessageHash", func(_, b map[string]any) { b["safeMessageHash"] = flipped }, false, "safeMessageHash " + flipped + " is not the Safe message hash"},
		{"signer is not a policy owner", func(_, _ map[string]any) {}, true, outsider.Hex() + " is not an owner of the Safe in the policy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			a, b := readShare(shareA), readShare(shareB)
			if tt.useX {
				b = readShare(shareX)
			}
			tt.edit(a, b)
			for name, m := range map[string]map[string]any{"a.json": a, "b.json": b} {
				raw, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(dir, name)
				if err := os.WriteFile(p, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			outPath := filepath.Join(dir, "combined.json")
			args := append([]string{"combine", "--policy", policyPath, "--out", outPath}, filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json"))
			out, err := runSigner(t, bin, "", args...)
			if err == nil {
				t.Fatalf("combine succeeded, want a refusal\n%s", out)
			}
			if !strings.Contains(out, tt.wantErr) {
				t.Errorf("output = %q\nwant it to contain %q", out, tt.wantErr)
			}
			if _, err := os.Stat(outPath); err == nil {
				t.Error("combined result must not be written")
			}
		})
	}
}

// Rebuild after each field edit so valid hashes/digests let the named binding check run
func TestE2E_PackageBindingsAreEnforcedIndividually(t *testing.T) {
	now := time.Now().UTC()
	flipped := "0x" + strings.Repeat("ab", 32)
	tests := []struct {
		name     string
		ov       func(burn bool) docOverrides
		mintOnly bool
		wantErr  string
	}{
		{
			name:    "metadata amount differs from typed-data amount",
			ov:      func(bool) docOverrides { return docOverrides{metadataAmount: "999"} },
			wantErr: "metadata issuance.amount 999 does not equal typed-data amount 1000000000000000000000",
		},
		{
			name: "typed-data profileDigest differs from the profile",
			ov: func(bool) docOverrides {
				return docOverrides{typedEdit: func(td map[string]any) { td["message"].(map[string]any)["profileDigest"] = flipped }}
			},
			wantErr: "typed-data.json message.profileDigest " + flipped + " does not match recomputed profileDigest",
		},
		{
			name: "typed-data metadataDigest differs from the metadata",
			ov: func(bool) docOverrides {
				return docOverrides{typedEdit: func(td map[string]any) { td["message"].(map[string]any)["metadataDigest"] = flipped }}
			},
			wantErr: "typed-data.json message.metadataDigest " + flipped + " does not match recomputed metadataDigest",
		},
		{
			name: "manifest metadataDigest differs from the metadata",
			ov: func(bool) docOverrides {
				return docOverrides{manifestEdit: func(m map[string]any) { m["metadataDigest"] = flipped }}
			},
			wantErr: "does not match manifest.json metadataDigest " + flipped,
		},
		{
			name: "manifest profileDigest differs from the profile",
			ov: func(bool) docOverrides {
				return docOverrides{manifestEdit: func(m map[string]any) { m["profileDigest"] = flipped }}
			},
			wantErr: "does not match manifest.json profileDigest " + flipped,
		},
		{
			name: "manifest primaryType differs from typed-data",
			ov: func(burn bool) docOverrides {
				other := "BurnAttestation"
				if burn {
					other = "MintAttestation"
				}
				return docOverrides{manifestEdit: func(m map[string]any) { m["primaryType"] = other }}
			},
			wantErr: "does not match manifest.json primaryType",
		},
		{
			name: "typed-data recordId differs from the metadata",
			ov: func(bool) docOverrides {
				return docOverrides{typedEdit: func(td map[string]any) {
					msg := td["message"].(map[string]any)
					msg["recordId"] = "OTHER-BAR-1"
					key := attestation.RecordKey("OTHER-BAR-1") // consistent, so only the recordId check can fire
					msg["recordKey"] = "0x" + hex.EncodeToString(key[:])
				}}
			},
			mintOnly: true,
			wantErr:  `typed-data.json message.recordId "OTHER-BAR-1" does not match metadata.json recordId "GOLD-BAR-12345"`,
		},
		{
			name: "typed-data recordKey is not keccak256 of the recordId",
			ov: func(bool) docOverrides {
				return docOverrides{typedEdit: func(td map[string]any) { td["message"].(map[string]any)["recordKey"] = flipped }}
			},
			mintOnly: true,
			wantErr:  "recomputed recordKey does not match typed-data.json message.recordKey",
		},
		{
			name: "validUntil beyond createdAt plus the policy lifetime",
			ov: func(bool) docOverrides {
				return docOverrides{createdAt: now.Add(-48 * time.Hour), validUntil: now.Add(29 * 24 * time.Hour)}
			},
			wantErr: "exceeds the maximum attestation lifetime",
		},
		{
			// createdAt later than now must not push the allowed window past now plus the lifetime
			name: "future createdAt does not extend the lifetime",
			ov: func(bool) docOverrides {
				return docOverrides{createdAt: now.Add(10 * 24 * time.Hour), validUntil: now.Add(35 * 24 * time.Hour)}
			},
			wantErr: "exceeds the maximum attestation lifetime",
		},
	}
	for _, tt := range tests {
		for _, burn := range []bool{false, true} {
			if burn && tt.mintOnly {
				continue
			}
			name := tt.name + "/mint"
			if burn {
				name = tt.name + "/burn"
			}
			t.Run(name, func(t *testing.T) {
				ov := tt.ov(burn)
				ov.burn = burn
				pkg := buildPackageWithOverrides(t, testChainID, testController, testVault, ov)
				dir := t.TempDir()
				policyPath := writePolicyFile(t, dir, testChainID, testController, testVault, testProjectID, pkg)
				out := signPackage(t, pkg, dir, policyPath, pkg.password+"\n", "--yes", "--unsafe-test-mode")
				if out.err == nil {
					t.Fatalf("sign succeeded, want a refusal\nstdout:\n%s", out.stdout)
				}
				if !strings.Contains(out.stderr, tt.wantErr) {
					t.Errorf("stderr = %q\nwant it to contain %q", out.stderr, tt.wantErr)
				}
				assertNoResult(t, dir)
			})
		}
	}
}
