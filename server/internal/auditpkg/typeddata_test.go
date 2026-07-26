package auditpkg

import (
	"bytes"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/rwa-platform/server/internal/eip712"
)

// loadTypedDataSchema compiles the FROZEN shared/schemas/typed-data.schema.json
// so this package's TypedDataDoc output is checked against the real schema
// the offline signer also validates against, not just our own Go structs.
func loadTypedDataSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	p := filepath.Join("..", "..", "..", "shared", "schemas", "typed-data.schema.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("shared schema not found: %v", err)
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
