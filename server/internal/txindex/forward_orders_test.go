package txindex

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/memory"
)

var escrowAddr = "0x00000000000000000000000000000000000000e5"

// evt is one log of a test transaction
type evt struct {
	name string
	data map[string]any
}

// The kinds, actors and values of the rwa-v4 order and forward-pricing events. A settle that
// fills emits PurchaseSettled together with Purchased and must stay a "purchase"; a settle
// that refunds emits PurchaseRefunded alone.
func TestProjectsForwardOrderEvents(t *testing.T) {
	tests := []struct {
		name      string
		address   string
		events    []evt
		wantKind  string
		wantFrom  string
		wantValue string
	}{
		{
			name:      "requestPurchase",
			address:   vaultAddr,
			events:    []evt{{"PurchaseRequested", map[string]any{"buyer": callerAddr, "recipient": senderAddr, "tokenAmount": "1000", "maxQuoteAmount": "2500"}}},
			wantKind:  "purchase_requested",
			wantFrom:  callerAddr,
			wantValue: "2500",
		},
		{
			name:    "settlePurchase that fills",
			address: vaultAddr,
			events: []evt{
				{"PurchaseSettled", map[string]any{"roundId": "7", "quoteAmount": "2400", "refund": "100"}},
				{"Purchased", map[string]any{"buyer": callerAddr, "recipient": senderAddr, "tokenAmount": "1000", "quoteAmount": "2400"}},
			},
			wantKind: "purchase",
			// the event names the buyer, who did not sign: with no node lookup the sender stays empty
			wantFrom:  "",
			wantValue: "2400",
		},
		{
			name:      "settlePurchase that refunds",
			address:   vaultAddr,
			events:    []evt{{"PurchaseRefunded", map[string]any{"roundId": "7", "quoteAmount": "2600", "refund": "2500"}}},
			wantKind:  "purchase_refunded",
			wantFrom:  "",
			wantValue: "2500",
		},
		{
			name:      "cancelPurchase",
			address:   vaultAddr,
			events:    []evt{{"PurchaseCancelled", map[string]any{"buyer": callerAddr, "refund": "2500"}}},
			wantKind:  "purchase_cancelled",
			wantFrom:  callerAddr,
			wantValue: "2500",
		},
		{
			name:      "rejectPurchase",
			address:   vaultAddr,
			events:    []evt{{"PurchaseRejected", map[string]any{"buyer": callerAddr, "refund": "2500"}}},
			wantKind:  "purchase_rejected",
			wantFrom:  "",
			wantValue: "2500",
		},
		{
			name:    "requestRedemption on an oracle",
			address: escrowAddr,
			events: []evt{
				{"RedemptionRequested", map[string]any{"beneficiary": callerAddr, "rwaAmount": "1000", "quoteAmount": "0"}},
				{"RedemptionForwardPriced", map[string]any{"strategy": senderAddr, "baseRound": "7", "minQuoteOut": "900"}},
			},
			wantKind:  "redemption_requested",
			wantFrom:  callerAddr,
			wantValue: "0",
		},
		{
			name:    "fundRedemption on an oracle",
			address: escrowAddr,
			events: []evt{
				{"RedemptionPriced", map[string]any{"roundId": "8", "quoteAmount": "950"}},
				{"RedemptionFunded", map[string]any{"funder": senderAddr, "quoteAmount": "950"}},
			},
			wantKind:  "redemption_funded",
			wantFrom:  senderAddr,
			wantValue: "950",
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repos := memory.New()
			txHash := "0xfwd" + string(rune('a'+i))
			for j, e := range tt.events {
				seedEvent(t, repos, tt.address, txHash, e.name, 10, uint(j), false, e.data)
			}
			run(t, repos)

			tx := getEvtTx(t, repos, txHash)
			if tx.Kind != tt.wantKind || tx.From != tt.wantFrom || tx.Value != tt.wantValue || tx.To != tt.address {
				t.Errorf("tx = kind %q from %q value %q to %q, want %q %q %q %q",
					tx.Kind, tx.From, tx.Value, tx.To, tt.wantKind, tt.wantFrom, tt.wantValue, tt.address)
			}
		})
	}
}

// the log order inside the transaction must not decide the kind
func TestSettleThatFillsStaysAPurchaseWhicheverEventLogsFirst(t *testing.T) {
	repos := memory.New()
	seedEvent(t, repos, vaultAddr, "0xfill", "Purchased", 10, 0, false,
		map[string]any{"buyer": callerAddr, "recipient": senderAddr, "tokenAmount": "1000", "quoteAmount": "2400"})
	seedEvent(t, repos, vaultAddr, "0xfill", "PurchaseSettled", 10, 1, false,
		map[string]any{"roundId": "7", "quoteAmount": "2400", "refund": "100"})
	run(t, repos)
	if tx := getEvtTx(t, repos, "0xfill"); tx.Kind != "purchase" || tx.Value != "2400" {
		t.Errorf("kind/value = %q/%q, want purchase/2400", tx.Kind, tx.Value)
	}
}

// The sender of a transaction whose events do not name the signer is the account the node
// reports, looked up once; a buyer's own transactions keep the buyer without any lookup
func TestUnnamedSignerIsLookedUpOnceAndNeverTheBuyer(t *testing.T) {
	keeper := "0x00000000000000000000000000000000000000ee"
	for name, events := range map[string][]evt{
		"settle that fills": {
			{"PurchaseSettled", map[string]any{"roundId": "7", "quoteAmount": "2400", "refund": "100"}},
			{"Purchased", map[string]any{"buyer": callerAddr, "recipient": senderAddr, "tokenAmount": "1000", "quoteAmount": "2400"}},
		},
		"settle that refunds": {{"PurchaseRefunded", map[string]any{"roundId": "7", "quoteAmount": "2600", "refund": "2500"}}},
		"reject":              {{"PurchaseRejected", map[string]any{"buyer": callerAddr, "refund": "2500"}}},
	} {
		t.Run(name, func(t *testing.T) {
			repos := memory.New()
			for j, e := range events {
				seedEvent(t, repos, vaultAddr, "0xsig", e.name, 10, uint(j), false, e.data)
			}
			var lookups int
			sender := func(_ context.Context, txHash string) (string, error) {
				lookups++
				if txHash != "0xsig" {
					t.Errorf("looked up %s", txHash)
				}
				return keeper, nil
			}

			runWith(t, repos, sender)
			runWith(t, repos, sender)

			if tx := getEvtTx(t, repos, "0xsig"); tx.From != keeper {
				t.Errorf("From = %q, want the signer %s", tx.From, keeper)
			}
			if lookups != 1 {
				t.Errorf("%d lookups, want 1: a resolved sender is kept", lookups)
			}
		})
	}

	t.Run("a buyer's own transactions need no lookup", func(t *testing.T) {
		repos := memory.New()
		seedEvent(t, repos, vaultAddr, "0xbuy", "Purchased", 10, 0, false,
			map[string]any{"buyer": callerAddr, "recipient": senderAddr, "tokenAmount": "1000", "quoteAmount": "2400"})
		seedEvent(t, repos, vaultAddr, "0xcancel", "PurchaseCancelled", 11, 0, false, map[string]any{"buyer": callerAddr, "refund": "2500"})
		runWith(t, repos, func(context.Context, string) (string, error) {
			t.Error("the node was asked for a sender the events already name")
			return keeper, nil
		})
		for _, h := range []string{"0xbuy", "0xcancel"} {
			if tx := getEvtTx(t, repos, h); tx.From != callerAddr {
				t.Errorf("%s From = %q, want the buyer %s", h, tx.From, callerAddr)
			}
		}
	})

	t.Run("a failed lookup leaves the sender empty and is retried", func(t *testing.T) {
		repos := memory.New()
		seedEvent(t, repos, vaultAddr, "0xretry", "PurchaseRejected", 10, 0, false, map[string]any{"buyer": callerAddr, "refund": "2500"})
		runWith(t, repos, func(context.Context, string) (string, error) { return "", errors.New("node unreachable") })
		if tx := getEvtTx(t, repos, "0xretry"); tx.From != "" {
			t.Fatalf("From = %q after a failed lookup, want empty", tx.From)
		}
		runWith(t, repos, func(context.Context, string) (string, error) { return keeper, nil })
		if tx := getEvtTx(t, repos, "0xretry"); tx.From != keeper {
			t.Errorf("From = %q after the retry, want %s", tx.From, keeper)
		}
	})
}

// Redemption claims are permissionless and the token's freeze and seizure calls are admin-only,
// yet their events name the beneficiary or the holder, not the account that signed
func TestRedemptionClaimAndTokenAdminSignersAreLookedUp(t *testing.T) {
	signer := "0x00000000000000000000000000000000000000ee"
	for _, tc := range []struct {
		name    string
		address string
		kind    string
		events  []evt
	}{
		{"claim for a beneficiary", escrowAddr, "redemption_claimed", []evt{
			{"RedemptionCompleted", map[string]any{"beneficiary": callerAddr, "rwaAmount": "1000", "quoteAmount": "950"}},
		}},
		{"freeze", tokenAddr, "Frozen", []evt{
			{"Frozen", map[string]any{"account": callerAddr, "amount": "500"}},
		}},
		{"seizure that spends frozen tokens", tokenAddr, "Frozen", []evt{
			{"Frozen", map[string]any{"account": callerAddr, "amount": "100"}},
			{"ForcedTransfer", map[string]any{"from": callerAddr, "to": senderAddr, "amount": "400"}},
		}},
		{"seizure of unfrozen tokens", tokenAddr, "ForcedTransfer", []evt{
			{"ForcedTransfer", map[string]any{"from": callerAddr, "to": senderAddr, "amount": "400"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos := memory.New()
			for j, e := range tc.events {
				seedEvent(t, repos, tc.address, "0xadmin", e.name, 10, uint(j), false, e.data)
			}
			var lookups int
			sender := func(context.Context, string) (string, error) {
				lookups++
				return signer, nil
			}

			runWith(t, repos, sender)
			runWith(t, repos, sender)

			tx := getEvtTx(t, repos, "0xadmin")
			if tx.From != signer {
				t.Errorf("From = %q, want the signer %s", tx.From, signer)
			}
			if tx.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", tx.Kind, tc.kind)
			}
			if lookups != 1 {
				t.Errorf("%d lookups, want 1: a resolved sender is kept", lookups)
			}
		})
	}
}

// An event that names the account whose call emitted it needs no lookup: a rejection carries
// its caller, and the other redemption steps are signed by the account they name
func TestEventsThatNameTheSignerNeedNoLookup(t *testing.T) {
	manager := common.HexToAddress("0x00000000000000000000000000000000000000a7").Hex()
	for name, e := range map[string]evt{
		"rejection":    {"RedemptionRejected", map[string]any{"reasonCode": "0x01", "caller": manager}},
		"funding":      {"RedemptionFunded", map[string]any{"funder": manager, "quoteAmount": "950"}},
		"request":      {"RedemptionRequested", map[string]any{"beneficiary": manager, "rwaAmount": "1000", "quoteAmount": "950"}},
		"cancel":       {"RedemptionCancelled", map[string]any{"beneficiary": manager}},
		"income claim": {"IncomeClaimed", map[string]any{"account": manager, "amount": "7"}},
	} {
		t.Run(name, func(t *testing.T) {
			repos := memory.New()
			seedEvent(t, repos, escrowAddr, "0xnamed", e.name, 10, 0, false, e.data)
			runWith(t, repos, func(context.Context, string) (string, error) {
				t.Error("the node was asked for a sender the event already names")
				return "0xother", nil
			})
			if tx := getEvtTx(t, repos, "0xnamed"); tx.From != manager {
				t.Errorf("From = %q, want %s", tx.From, manager)
			}
		})
	}
}

func TestSenderFromNodeRecoversTheSigner(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := types.SignNewTx(key, types.LatestSignerForChainID(big.NewInt(txChainID)), &types.DynamicFeeTx{
		ChainID:   big.NewInt(txChainID),
		Gas:       21000,
		GasFeeCap: big.NewInt(1),
		GasTipCap: big.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := blockchain.NewFakeClient()
	client.SetTx(signed.Hash(), signed)
	lookup := SenderFromNode(client, txChainID)

	got, err := lookup(context.Background(), signed.Hash().Hex())
	if want := crypto.PubkeyToAddress(key.PublicKey).Hex(); err != nil || got != want {
		t.Errorf("sender = %q, %v; want %s", got, err, want)
	}
	if _, err := lookup(context.Background(), "0xabsent"); err == nil {
		t.Error("an unknown transaction produced a sender")
	}
}
