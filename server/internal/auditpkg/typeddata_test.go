package auditpkg

import (
	"bytes"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/rwa-platform/server/internal/eip712"
)

// loadTypedDataSchema compiles shared/schemas/typed-data.schema.json so
// TypedDataDoc output is checked against the schema itself and not only against
// the Go structs
func loadTypedDataSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	p := filepath.Join("..", "..", "..", "shared", "schemas", "typed-data.schema.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("shared schema not found: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("typed-data.json", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("typed-data.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMintTypedDataDocConformsToSchema(t *testing.T) {
	schema := loadTypedDataSchema(t)

	domain := eip712.Domain{
		Name:              "RWA-Supply-Attestation",
		Version:           "1",
		ChainID:           big.NewInt(31337),
		VerifyingContract: common.HexToAddress("0x5FbDB2315678afecb367f032d93F642f64180aa"),
	}
	a := eip712.MintAttestation{
		Auditor:        common.HexToAddress("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb9226"),
		ProfileDigest:  [32]byte{0x11},
		RecordKey:      [32]byte{0x22},
		MetadataDigest: [32]byte{0x33},
		Amount:         big.NewInt(1000000000000000000),
		Nonce:          big.NewInt(42),
		ValidUntil:     1800000000,
		Vault:          common.HexToAddress("0xe7f1725E7734CE288F8367e1Bb143E90bb3F051"),
	}
	doc := NewMintTypedDataDoc(domain, "GOLD-BAR-12345", a)
	m := doc.Message
	if doc.PrimaryType != "MintAttestation" || doc.Domain.Name != domain.Name || doc.Domain.ChainID != 31337 ||
		doc.Domain.VerifyingContract != domain.VerifyingContract.Hex() {
		t.Errorf("domain/primaryType = %+v / %s", doc.Domain, doc.PrimaryType)
	}
	if m.Auditor != a.Auditor.Hex() || m.Vault != a.Vault.Hex() || m.RecordID != "GOLD-BAR-12345" ||
		m.RecordKey != "0x22"+strings.Repeat("00", 31) || m.ProfileDigest != "0x11"+strings.Repeat("00", 31) ||
		m.MetadataDigest != "0x33"+strings.Repeat("00", 31) || m.Amount != "1000000000000000000" || m.Nonce != "42" ||
		m.ValidUntil != 1800000000 || m.OperationID != "" {
		t.Errorf("mint message fields mapped wrongly: %+v", m)
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("MintAttestation typed-data.json does not conform to schema: %v\ndoc: %s", err, raw)
	}
}

func TestBurnTypedDataDocConformsToSchema(t *testing.T) {
	schema := loadTypedDataSchema(t)

	domain := eip712.Domain{
		Name:              "RWA-Supply-Attestation",
		Version:           "1",
		ChainID:           big.NewInt(31337),
		VerifyingContract: common.HexToAddress("0x5FbDB2315678afecb367f032d93F642f64180aa"),
	}
	a := eip712.BurnAttestation{
		Auditor:        common.HexToAddress("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb9226"),
		ProfileDigest:  [32]byte{0x11},
		OperationID:    [32]byte{0x44},
		MetadataDigest: [32]byte{0x33},
		Amount:         big.NewInt(500),
		Nonce:          big.NewInt(43),
		ValidUntil:     1800000000,
		Vault:          common.HexToAddress("0xe7f1725E7734CE288F8367e1Bb143E90bb3F051"),
	}
	doc := NewBurnTypedDataDoc(domain, a)
	m := doc.Message
	if doc.PrimaryType != "BurnAttestation" {
		t.Errorf("primaryType = %s", doc.PrimaryType)
	}
	if m.Auditor != a.Auditor.Hex() || m.Vault != a.Vault.Hex() || m.OperationID != "0x44"+strings.Repeat("00", 31) ||
		m.ProfileDigest != "0x11"+strings.Repeat("00", 31) || m.MetadataDigest != "0x33"+strings.Repeat("00", 31) ||
		m.Amount != "500" || m.Nonce != "43" || m.ValidUntil != 1800000000 || m.RecordKey != "" || m.RecordID != "" {
		t.Errorf("burn message fields mapped wrongly: %+v", m)
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("BurnAttestation typed-data.json does not conform to schema: %v\ndoc: %s", err, raw)
	}
}
