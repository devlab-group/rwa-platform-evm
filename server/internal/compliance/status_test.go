package compliance

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/memory"
)

func TestStatusServiceSetStatusSubmitsExpectedCalldata(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	contract := common.HexToAddress("0x0000000000000000000000000000000000C0DE")
	client := blockchain.NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	txs := blockchain.NewTxManager(client, txRepo, big.NewInt(31337), blockchain.FeeModeLegacy)

	svc := NewStatusService(txs, contract, blockchain.NewStaticKeySigner(priv))
	account := common.HexToAddress("0x000000000000000000000000000000000000B1")

	tx, err := svc.SetStatus(context.Background(), "", account, bindings.ComplianceStatusAllowed, 999)
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if tx.To != contract.Hex() {
		t.Errorf("To = %s, want %s", tx.To, contract.Hex())
	}
	if tx.Kind != "compliance.setStatus" {
		t.Errorf("Kind = %q", tx.Kind)
	}

	registry := bindings.NewComplianceRegistry()
	wantData, err := registry.PackSetStatus(account, bindings.ComplianceStatusAllowed, 999)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Data != "0x"+common.Bytes2Hex(wantData) {
		t.Errorf("Data mismatch")
	}
}

func TestStatusFromString(t *testing.T) {
	cases := map[string]bindings.ComplianceStatus{"Unknown": bindings.ComplianceStatusUnknown, "Allowed": bindings.ComplianceStatusAllowed, "Blocked": bindings.ComplianceStatusBlocked}
	for s, want := range cases {
		got, ok := StatusFromString(s)
		if !ok || got != want {
			t.Errorf("StatusFromString(%q) = %v,%v want %v,true", s, got, ok, want)
		}
	}
	if _, ok := StatusFromString("bogus"); ok {
		t.Error("expected ok=false for unknown status string")
	}
}
