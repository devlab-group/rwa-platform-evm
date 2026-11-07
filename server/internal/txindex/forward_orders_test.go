package txindex

import (
	"testing"

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
			wantKind:  "purchase",
			wantFrom:  callerAddr,
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
			wantFrom:  callerAddr,
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
