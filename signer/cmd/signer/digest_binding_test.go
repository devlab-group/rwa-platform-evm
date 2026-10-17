package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	"github.com/rwa-platform/signer/internal/attestation"
)

// Compare CLI digests with independent encoding to expose dropped or swapped typed-data fields

type fieldMap = map[string]any

func loadMintVectorFile(t *testing.T) (domain, message fieldMap, digest, privKey, signature string) {
	t.Helper()
	return loadVectorFile(t, "mint-eip712.json")
}

// loadVectorFile reads a shared/vectors attestation file with numbers kept exact
func loadVectorFile(t *testing.T, name string) (domain, message fieldMap, digest, privKey, signature string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "shared", "vectors", name))
	if err != nil {
		t.Fatalf("reading vector %s: %v", name, err)
	}
	var v struct {
		Domain           fieldMap `json:"domain"`
		Message          fieldMap `json:"message"`
		Digest           string   `json:"digest"`
		SignerPrivateKey string   `json:"signerPrivateKey"`
		Signature        string   `json:"signature"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decoding mint vector: %v", err)
	}
	return v.Domain, v.Message, v.Digest, v.SignerPrivateKey, v.Signature
}

var (
	mintFields = []string{"auditor", "profileDigest", "recordKey", "metadataDigest", "amount", "nonce", "validUntil", "vault"}
	burnFields = []string{"auditor", "profileDigest", "operationId", "metadataDigest", "amount", "nonce", "validUntil", "vault"}
)

func fieldTypes(names []string) []apitypes.Type {
	solType := map[string]string{
		"auditor": "address", "vault": "address", "amount": "uint256", "nonce": "uint256", "validUntil": "uint64",
	}
	out := make([]apitypes.Type, len(names))
	for i, n := range names {
		typ, ok := solType[n]
		if !ok {
			typ = "bytes32"
		}
		out[i] = apitypes.Type{Name: n, Type: typ}
	}
	return out
}

// oracleDigest uses go-ethereum's generic EIP-712 encoder instead of internal/attestation
func oracleDigest(t *testing.T, primary string, domain, message fieldMap) common.Hash {
	t.Helper()
	names := mintFields
	if primary == "BurnAttestation" {
		names = burnFields
	}
	chainID, ok := new(big.Int).SetString(fmt.Sprint(domain["chainId"]), 10)
	if !ok {
		t.Fatalf("oracle: bad chainId %v", domain["chainId"])
	}
	msg := apitypes.TypedDataMessage{}
	for _, n := range names {
		msg[n] = fmt.Sprint(message[n])
	}
	td := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"}, {Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"}, {Name: "verifyingContract", Type: "address"},
			},
			primary: fieldTypes(names),
		},
		PrimaryType: primary,
		Domain: apitypes.TypedDataDomain{
			Name:              fmt.Sprint(domain["name"]),
			Version:           fmt.Sprint(domain["version"]),
			ChainId:           (*math.HexOrDecimal256)(chainID),
			VerifyingContract: fmt.Sprint(domain["verifyingContract"]),
		},
		Message: msg,
	}
	hash, _, err := apitypes.TypedDataAndHash(td)
	if err != nil {
		t.Fatalf("oracle TypedDataAndHash: %v", err)
	}
	return common.BytesToHash(hash)
}

func cloneFields(m fieldMap) fieldMap {
	out := make(fieldMap, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// cliDigest feeds typed-data JSON through the CLI's own parser and digest mapping
func cliDigest(t *testing.T, primary string, domain, message fieldMap) common.Hash {
	t.Helper()
	message = cloneFields(message)
	delete(message, "burnId") // vector-only input of operationId, not a typed-data field
	raw, err := json.Marshal(fieldMap{"primaryType": primary, "domain": domain, "message": message})
	if err != nil {
		t.Fatal(err)
	}
	td, err := parseTypedData(raw)
	if err != nil {
		t.Fatalf("parseTypedData: %v\n%s", err, raw)
	}
	var d common.Hash
	if primary == "BurnAttestation" {
		d, err = attestation.BurnDigest(td.domain(), td.burnAttestation())
	} else {
		d, err = attestation.MintDigest(td.domain(), td.mintAttestation())
	}
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return d
}

// changes returns one-field-at-a-time edits of a message and domain, plus swaps of
// same-typed fields
func changes(primary string, domain, message fieldMap) []struct {
	name    string
	domain  fieldMap
	message fieldMap
} {
	type row = struct {
		name    string
		domain  fieldMap
		message fieldMap
	}
	other := map[string]any{
		"auditor": "0x70997970C51812dc3A010C7d01b50e0d17dc79C8", "vault": "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC",
		"profileDigest": "0x" + strings.Repeat("aa", 32), "recordKey": "0x" + strings.Repeat("bb", 32),
		"operationId": "0x" + strings.Repeat("bb", 32), "metadataDigest": "0x" + strings.Repeat("cc", 32),
		"amount": "1000000000000000000001", "nonce": "1", "validUntil": json.Number("1800000001"),
	}
	var rows []row
	names := mintFields
	if primary == "BurnAttestation" {
		names = burnFields
	}
	for _, n := range names {
		m := cloneFields(message)
		m[n] = other[n]
		rows = append(rows, row{"message." + n, domain, m})
	}
	d := cloneFields(domain)
	d["chainId"] = json.Number("31338")
	rows = append(rows, row{"domain.chainId", d, message})
	d = cloneFields(domain)
	d["verifyingContract"] = "0x70997970C51812dc3A010C7d01b50e0d17dc79C8"
	rows = append(rows, row{"domain.verifyingContract", d, message})

	third := "recordKey"
	if primary == "BurnAttestation" {
		third = "operationId"
	}
	for _, pair := range [][2]string{
		{"profileDigest", "metadataDigest"}, {"profileDigest", third}, {third, "metadataDigest"},
		{"auditor", "vault"}, {"amount", "nonce"},
	} {
		m := cloneFields(message)
		m[pair[0]], m[pair[1]] = message[pair[1]], message[pair[0]]
		rows = append(rows, row{"swap " + pair[0] + " and " + pair[1], domain, m})
	}
	return rows
}

// Map the frozen mint vector through the CLI's parser and require its digest literal
func TestTypedDataMapping_MintReproducesVectorDigest(t *testing.T) {
	domain, message, wantDigest, _, _ := loadMintVectorFile(t)
	if got := oracleDigest(t, "MintAttestation", domain, message); got.Hex() != wantDigest {
		t.Fatalf("independent oracle digest = %s, vector digest = %s", got.Hex(), wantDigest)
	}
	if got := cliDigest(t, "MintAttestation", domain, message); got.Hex() != wantDigest {
		t.Fatalf("CLI mapped digest = %s, vector digest = %s", got.Hex(), wantDigest)
	}
}

// Map the frozen burn vector through the CLI's parser and require its digest literal
func TestTypedDataMapping_BurnReproducesVectorDigest(t *testing.T) {
	domain, message, wantDigest, _, _ := loadVectorFile(t, "burn-eip712.json")
	if got := oracleDigest(t, "BurnAttestation", domain, message); got.Hex() != wantDigest {
		t.Fatalf("independent oracle digest = %s, vector digest = %s", got.Hex(), wantDigest)
	}
	if got := cliDigest(t, "BurnAttestation", domain, message); got.Hex() != wantDigest {
		t.Fatalf("CLI mapped digest = %s, vector digest = %s", got.Hex(), wantDigest)
	}
}

// Changing or swapping any signed field must change the CLI digest, and the new digest
// must be the one the independent encoder computes
func TestTypedDataMapping_EveryFieldIsBound(t *testing.T) {
	domain, mintMsg, mintVectorDigest, _, _ := loadMintVectorFile(t)
	_, burnMsg, burnVectorDigest, _, _ := loadVectorFile(t, "burn-eip712.json")
	baseline := map[string]common.Hash{
		"MintAttestation": common.HexToHash(mintVectorDigest),
		"BurnAttestation": common.HexToHash(burnVectorDigest),
	}
	for _, primary := range []string{"MintAttestation", "BurnAttestation"} {
		msg := mintMsg
		if primary == "BurnAttestation" {
			msg = burnMsg
		}
		for _, row := range changes(primary, domain, msg) {
			t.Run(primary+"/"+row.name, func(t *testing.T) {
				got := cliDigest(t, primary, row.domain, row.message)
				if got == baseline[primary] {
					t.Fatalf("digest unchanged (%s) after changing %s", got, row.name)
				}
				if want := oracleDigest(t, primary, row.domain, row.message); got != want {
					t.Fatalf("CLI digest %s != independent digest %s after changing %s", got, want, row.name)
				}
			})
		}
	}
}

// signOutcome is the result of one CLI run
type signOutcome struct {
	stdout, stderr string
	err            error
}

// signPackage writes pkg into workDir/request.rwa and runs "signer sign" on it with the
// package keystore, the policy at policyPath and the given extra flags
func signPackage(t *testing.T, pkg builtPackage, workDir, policyPath, stdin string, extra ...string) signOutcome {
	t.Helper()
	bin := buildSignerBinary(t, findGoBinary(t), "")
	pkgPath := filepath.Join(workDir, "request.rwa")
	if err := os.WriteFile(pkgPath, pkg.zipBytes, 0o644); err != nil {
		t.Fatalf("writing package: %v", err)
	}
	args := append([]string{"sign", pkgPath, "--keystore", pkg.keystorePath, "--policy", policyPath}, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return signOutcome{stdout.String(), stderr.String(), err}
}

func readSignedResult(t *testing.T, path string) (auditor, primary, digest, sig string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var r struct {
		Auditor         string `json:"auditor"`
		PrimaryType     string `json:"primaryType"`
		TypedDataDigest string `json:"typedDataDigest"`
		Signature       string `json:"signature"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return r.Auditor, r.PrimaryType, r.TypedDataDigest, r.Signature
}

// Sign mint/burn packages with the vector key and compare CLI digests and deterministic
// RFC 6979 signatures against an independent encoder/signer.
//
// The vector's placeholder digests (0x11.., 0x22..) lack matching source documents for
// packaging; the CLI recomputes those hashes. In-process mapping tests pin the vector
// digest; packaged tests use relative validUntil to avoid eventual expiry of 1800000000.
func TestE2E_SignedDigestMatchesIndependentEncoding(t *testing.T) {
	_, _, _, privHex, _ := loadMintVectorFile(t)
	key, err := crypto.HexToECDSA(strings.TrimPrefix(privHex, "0x"))
	if err != nil {
		t.Fatalf("vector key: %v", err)
	}
	_, burnMsg, _, _, _ := loadVectorFile(t, "burn-eip712.json")
	for _, primary := range []string{"MintAttestation", "BurnAttestation"} {
		t.Run(primary, func(t *testing.T) {
			pkg := buildPackageWithOverrides(t, testChainID, testController, testVault,
				docOverrides{key: key, burn: primary == "BurnAttestation", nonce: "43", operationID: fmt.Sprint(burnMsg["operationId"])})
			workDir := t.TempDir()
			policyPath := writePolicyFile(t, workDir, testChainID, testController, testVault, testProjectID, pkg)
			out := signPackage(t, pkg, workDir, policyPath, pkg.password+"\n", "--yes", "--unsafe-test-mode")
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
			want := oracleDigest(t, primary, td.Domain, td.Message)
			auditor, gotPrimary, gotDigest, gotSig := readSignedResult(t, filepath.Join(workDir, "signed-result.json"))
			if gotPrimary != primary {
				t.Errorf("primaryType = %q, want %q", gotPrimary, primary)
			}
			if gotDigest != want.Hex() {
				t.Errorf("typedDataDigest = %s, independent digest = %s", gotDigest, want.Hex())
			}
			if auditor != crypto.PubkeyToAddress(key.PublicKey).Hex() {
				t.Errorf("auditor = %s, want the vector key's address", auditor)
			}
			wantSig, err := crypto.Sign(want.Bytes(), key)
			if err != nil {
				t.Fatal(err)
			}
			wantSig[64] += 27
			if gotSig != fmt.Sprintf("0x%x", wantSig) {
				t.Errorf("signature = %s, want deterministic 0x%x", gotSig, wantSig)
			}
		})
	}
}

// A parsed typed-data document always maps to attestation values without panicking
func FuzzParseTypedData(f *testing.F) {
	for _, name := range []string{"mint-eip712.json", "burn-eip712.json"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "shared", "vectors", name))
		if err != nil {
			f.Fatal(err)
		}
		var v struct {
			Domain  fieldMap `json:"domain"`
			Message fieldMap `json:"message"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			f.Fatal(err)
		}
		delete(v.Message, "burnId")
		primary := "MintAttestation"
		if name == "burn-eip712.json" {
			primary = "BurnAttestation"
		}
		seed, err := json.Marshal(fieldMap{"primaryType": primary, "domain": v.Domain, "message": v.Message})
		if err != nil {
			f.Fatal(err)
		}
		f.Add(seed)
	}
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"primaryType":"MintAttestation"} trailing`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		td, err := parseTypedData(raw)
		if err != nil {
			return
		}
		d := td.domain()
		if td.PrimaryType == "BurnAttestation" {
			_, _ = attestation.BurnDigest(d, td.burnAttestation())
		} else {
			_, _ = attestation.MintDigest(d, td.mintAttestation())
		}
	})
}
