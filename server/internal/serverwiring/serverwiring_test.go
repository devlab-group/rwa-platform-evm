package serverwiring

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

// TestCheckChainIDRejectsWrongChain covers the wrong-chain guard: the
// exact condition every chain-touching opsctl command (and the platform's own
// connectChain) passes through must reject an endpoint reporting a chain ID
// other than the configured one.
func TestCheckChainIDRejectsWrongChain(t *testing.T) {
	client := blockchain.NewFakeClient()
	client.ChainIDValue = big.NewInt(1) // mainnet, not the configured 31337

	if err := CheckChainID(context.Background(), client, 31337); !errors.Is(err, ErrWrongChain) {
		t.Fatalf("CheckChainID = %v, want ErrWrongChain", err)
	}
	// Matching chain passes.
	client.ChainIDValue = big.NewInt(31337)
	if err := CheckChainID(context.Background(), client, 31337); err != nil {
		t.Fatalf("CheckChainID on the correct chain: %v", err)
	}
}

func controllerAddrs(addr common.Address) models.Addresses {
	return models.Addresses{SupplyController: addr.Hex()}
}

// mintedLog builds a canonical SupplyController Minted log at addr.
func mintedLog(t *testing.T, addr common.Address) types.Log {
	t.Helper()
	controller := bindings.NewSupplyController()
	data, err := controller.ABI.Events["Minted"].Inputs.NonIndexed().Pack(big.NewInt(100), big.NewInt(1), common.HexToAddress("0x00000000000000000000000000000000000A0D17"))
	if err != nil {
		t.Fatal(err)
	}
	vault := common.HexToAddress("0x0000000000000000000000000000000000005a17")
	return types.Log{
		Address: addr,
		Topics: []common.Hash{
			controller.EventID("Minted"),
			common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
			common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
			common.BytesToHash(vault.Bytes()),
		},
		Data: data,
	}
}

// TestStrictDecoderTypedEventPasses: a real typed event from a
// configured contract decodes to its name, so a DLQ retry can resolve it.
func TestStrictDecoderTypedEventPasses(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000c0d10")
	dec := StrictDecoder(controllerAddrs(addr), "")
	name, _, err := dec(mintedLog(t, addr))
	if err != nil {
		t.Fatalf("StrictDecoder on a typed Minted event: %v", err)
	}
	if name != "Minted" {
		t.Fatalf("name = %q, want Minted", name)
	}
}

// TestStrictDecoderRefusesGeneric is the core guard: a log that only
// decodes generically (unrecognized topic on a configured address, or any log
// on an UNconfigured address) returns ErrGenericDecode, so opsctl's DLQ retry
// will NOT resolve it with a meaningless generic event.
func TestStrictDecoderRefusesGeneric(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000c0d10")
	dec := StrictDecoder(controllerAddrs(addr), "")

	// Unrecognized topic on the configured controller address.
	unknownTopic := types.Log{Address: addr, Topics: []common.Hash{common.HexToHash("0xdeadbeef")}}
	if _, _, err := dec(unknownTopic); !errors.Is(err, ErrGenericDecode) {
		t.Fatalf("unrecognized topic: err = %v, want ErrGenericDecode", err)
	}

	// A log from an address no configured contract owns.
	otherAddr := common.HexToAddress("0x00000000000000000000000000000000000bad00")
	if _, _, err := dec(mintedLog(t, otherAddr)); !errors.Is(err, ErrGenericDecode) {
		t.Fatalf("log from unconfigured address: err = %v, want ErrGenericDecode", err)
	}
}

// TestKnownEventNamesEmptyWhenNoContract: with no typed
// contract configured, only the generic decoder is available — which is what
// opsctl uses to refuse `dlq retry` outright.
func TestKnownEventNamesEmptyWhenNoContract(t *testing.T) {
	if len(KnownEventNames(models.Addresses{}, "")) != 0 {
		t.Fatal("expected no known event names with no contract configured")
	}
	addr := common.HexToAddress("0x00000000000000000000000000000000000c0d10")
	known := KnownEventNames(controllerAddrs(addr), "")
	if !known["Minted"] || !known["Burned"] || !known["AuditorChanged"] {
		t.Fatalf("expected the SupplyController events to be known, got %v", known)
	}
}

// TestNewTxManagerMongoLeaseRefusesWhenLeaseHeld covers the concurrent
// CLI/server replacement guard: a manager built for mongo-lease mode (as
// opsctl's `tx replace` now builds it) must refuse to replace while another
// replica holds the signer's nonce lease, instead of racing it on the same
// nonce.
func TestNewTxManagerMongoLeaseRefusesWhenLeaseHeld(t *testing.T) {
	repos := memory.New()
	client := blockchain.NewFakeClient()
	priv, _ := crypto.GenerateKey()
	signer := blockchain.NewStaticKeySigner(priv)
	ctx := context.Background()
	from, err := signer.Address(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{ChainID: 31337, TxCoordinationMode: "mongo-lease", TxLeaseTTL: time.Minute, FeeMode: "legacy"}
	mgr := NewTxManager(cfg, client, repos)

	// Submit an original while the lease is free.
	original, err := mgr.Submit(ctx, blockchain.SubmitRequest{PrivateKey: priv, To: common.HexToAddress("0x00000000000000000000000000000000000000AA")})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Simulate a live server replica holding this signer's nonce lease (the
	// key format is chainId+":"+signerAddress — see blockchain.txManager.leaseKey).
	leaseKey := "31337:" + from.Hex()
	if _, ok, err := repos.NonceLeases.Acquire(ctx, leaseKey, "other-replica", time.Minute); err != nil || !ok {
		t.Fatalf("pre-acquire lease as another replica: ok=%v err=%v", ok, err)
	}

	sent := len(client.SentTxs)
	if _, err := mgr.Replace(ctx, original.ID, signer, 20); !errors.Is(err, blockchain.ErrLeaseUnavailable) {
		t.Fatalf("Replace = %v, want ErrLeaseUnavailable while another replica holds the lease", err)
	}
	if len(client.SentTxs) != sent {
		t.Fatalf("a lease-blocked replacement must not broadcast, got %d new sends", len(client.SentTxs)-sent)
	}
}
