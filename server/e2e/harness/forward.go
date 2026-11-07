package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/bindings"
)

// The rwa-v4 forward-pricing flow. The Vault is created on a Fixed strategy, so the harness
// first points it at the OraclePriceStrategy that run_e2e.sh deployed over a mock feed, then
// drives four orders through the real chain, indexer and API:
//
//   - a purchase that is requested, waits out the dealing cut-off, is priced by the next
//     round and settled by a third party;
//   - a redemption that is requested, priced by the round that lands after the cut-off when
//     the treasurer funds it, and claimed;
//   - a purchase that the treasurer rejects during a pause;
//   - a purchase settled by the real keeper binary once its round is published.
//
// Expected quotes are literals worked out from the feed answers and the strategy's 1%
// spreads (18-decimal token, 6-decimal quote, 8-decimal feed), not read back from the
// contract under test.

// the mock feed's and the token's mutating ABIs; every other call is encoded by internal/bindings
var (
	feedABI = mustABIJSON(`[
  {"type":"function","name":"setAnswer","stateMutability":"nonpayable","inputs":[{"name":"answer_","type":"int256"}],"outputs":[]},
  {"type":"function","name":"latestId","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint80"}]}
]`)
	pauseABI = mustABIJSON(`[
  {"type":"function","name":"pause","stateMutability":"nonpayable","inputs":[],"outputs":[]},
  {"type":"function","name":"unpause","stateMutability":"nonpayable","inputs":[],"outputs":[]}
]`)
)

const (
	purchaseDeposit      = 40_000_000 // 40 quote units, more than the order can cost
	purchaseQuote        = 26_260_000 // 10 RWA at 2.6 with a 1% markup
	purchaseRefund       = purchaseDeposit - purchaseQuote
	redemptionQuote      = 11_880_000 // 5 RWA at 2.4 with a 1% discount
	rejectedDeposit      = 5_000_000
	keeperDeposit        = 5_000_000
	keeperQuote          = 2_626_000   // 1 RWA at 2.6 with a 1% markup
	priceAfterPurchase   = 260_000_000 // 2.6 in 8 feed decimals
	priceAfterRedemption = 240_000_000 // 2.4 in 8 feed decimals
)

// orderJSON is components.schemas.PurchaseOrder
type orderJSON struct {
	ID             string
	Buyer          string
	Recipient      string
	TokenAmount    string
	MaxQuoteAmount string
	Strategy       string
	BaseRound      string
	Status         string
	CreatedAt      int64
	TimeoutAt      int64
	PricedRound    string
	QuoteAmount    string
	Refund         string
	RequestTxHash  string
	CloseTxHash    string
}

// redemptionJSON is the forward-pricing part of components.schemas.Redemption
type redemptionJSON struct {
	Status      string
	Claimable   bool
	QuoteAmount string
	Strategy    string
	BaseRound   string
	MinQuoteOut string
	PricedRound string
}

func forwardPricingLifecycle(ctx context.Context, api *apiClient, chain *chainClient, cfg config, adminKey *ecdsa.PrivateKey, admin common.Address, investorKey *ecdsa.PrivateKey, investor common.Address) error {
	step("replacement strategy: a price set before the swap is backfilled")
	if err := useReplacementStrategy(ctx, api, chain, cfg, adminKey); err != nil {
		return fmt.Errorf("replacement strategy: %w", err)
	}
	if err := useOracleStrategy(ctx, api, chain, cfg, adminKey); err != nil {
		return fmt.Errorf("setStrategy: %w", err)
	}
	step("forward purchase: requestPurchase, cut-off, new round, settlePurchase by a third party")
	if err := forwardPurchase(ctx, api, chain, cfg, adminKey, investorKey, investor); err != nil {
		return fmt.Errorf("forward purchase: %w", err)
	}
	step("forward redemption: requestRedemption, cut-off, new round, fundRedemption prices it, claim")
	if err := forwardRedemption(ctx, api, chain, cfg, adminKey, admin, investorKey, investor); err != nil {
		return fmt.Errorf("forward redemption: %w", err)
	}
	step("purchase rejected by the treasurer during a pause")
	if err := rejectedPurchase(ctx, api, chain, cfg, adminKey, investorKey, investor); err != nil {
		return fmt.Errorf("rejected purchase: %w", err)
	}
	step("keeper binary settles a purchase once its round is published")
	if err := keeperPurchase(ctx, api, chain, cfg, adminKey, investorKey, investor); err != nil {
		return fmt.Errorf("keeper purchase: %w", err)
	}
	return nil
}

// useOracleStrategy sends Vault.setStrategy and waits for the project to report the oracle
// strategy, its forward pricing and its dealing cut-off, and for the inventory to report no
// open deposits
func useOracleStrategy(ctx context.Context, api *apiClient, chain *chainClient, cfg config, adminKey *ecdsa.PrivateKey) error {
	data, err := bindings.NewVault().ABI.Pack("setStrategy", cfg.oracleStrategy)
	if err != nil {
		return err
	}
	if _, err := chain.sendTx(ctx, adminKey, cfg.vaultAddr, data); err != nil {
		return fmt.Errorf("submit Vault.setStrategy: %w", err)
	}
	// the strategy kind and the cut-off are live reads, the strategy address follows the
	// security projection (15s reconcile), and GET /project caches its reads for 15s
	if err := poll("GET /project to report the oracle strategy as forward priced", 120*time.Second, 3*time.Second, func() (bool, error) {
		var proj projectResponse
		if err := api.getJSON("/api/v1/project", "", &proj); err != nil {
			return false, err
		}
		if !strings.EqualFold(proj.Addresses.Strategy, cfg.oracleStrategy.Hex()) {
			return false, fmt.Errorf("addresses.strategy = %s, want %s", proj.Addresses.Strategy, cfg.oracleStrategy.Hex())
		}
		if proj.StrategyKind != "Oracle" {
			return false, fmt.Errorf("strategyKind = %q, want Oracle", proj.StrategyKind)
		}
		if proj.ForwardPriced == nil || !*proj.ForwardPriced {
			return false, fmt.Errorf("forwardPriced = %v, want true", proj.ForwardPriced)
		}
		if proj.Oracle == nil || proj.Oracle.DealingCutoff == nil || *proj.Oracle.DealingCutoff != cfg.dealingCutoff {
			return false, fmt.Errorf("oracle = %+v, want dealingCutoff %d", proj.Oracle, cfg.dealingCutoff)
		}
		return true, nil
	}); err != nil {
		return err
	}
	return expectPendingDeposits(api, "0")
}

func expectPendingDeposits(api *apiClient, want string) error {
	return poll("inventory pendingPurchaseQuote = "+want, 30*time.Second, 2*time.Second, func() (bool, error) {
		var inv struct{ PendingPurchaseQuote *string }
		if err := api.getJSON("/api/v1/sales/inventory", "", &inv); err != nil {
			return false, err
		}
		if inv.PendingPurchaseQuote == nil || *inv.PendingPurchaseQuote != want {
			return false, fmt.Errorf("pendingPurchaseQuote = %v, want %s", inv.PendingPurchaseQuote, want)
		}
		return true, nil
	})
}

// chain helpers

// feedRound reads the mock feed's latest round id
func (cc *chainClient) feedRound(ctx context.Context, feed common.Address) (*big.Int, error) {
	data, err := feedABI.Pack("latestId")
	if err != nil {
		return nil, err
	}
	out, err := cc.call(ctx, feed, data)
	if err != nil {
		return nil, fmt.Errorf("feed.latestId: %w", err)
	}
	return new(big.Int).SetBytes(out), nil
}

// publishAfterCutoff moves the chain clock past the dealing cut-off and publishes a round with
// answer, so the round lands at least cfg.dealingCutoff after every order already placed
func (cc *chainClient) publishAfterCutoff(ctx context.Context, cfg config, key *ecdsa.PrivateKey, answer int64) error {
	if err := cc.increaseTime(ctx, cfg.dealingCutoff+60); err != nil {
		return fmt.Errorf("evm_increaseTime: %w", err)
	}
	data, err := feedABI.Pack("setAnswer", big.NewInt(answer))
	if err != nil {
		return err
	}
	if _, err := cc.sendTx(ctx, key, cfg.oracleFeed, data); err != nil {
		return fmt.Errorf("publish round: %w", err)
	}
	return nil
}

// deadline is an hour past the chain clock, which can be ahead of the wall clock after earlier jumps
func (cc *chainClient) deadline(ctx context.Context) (uint64, error) {
	hdr, err := cc.eth.HeaderByNumber(ctx, nil)
	if err != nil {
		return 0, err
	}
	return hdr.Time + 3600, nil
}

func (cc *chainClient) blockTime(ctx context.Context, receipt *types.Receipt) (uint64, error) {
	hdr, err := cc.eth.HeaderByNumber(ctx, receipt.BlockNumber)
	if err != nil {
		return 0, err
	}
	return hdr.Time, nil
}

// expectRevert requires that the eth_call fails, as a contract revert does until a pricing round exists
func (cc *chainClient) expectRevert(ctx context.Context, from, to common.Address, data []byte, what string) error {
	if _, err := cc.eth.CallContract(ctx, ethereum.CallMsg{From: from, To: &to, Data: data}, nil); err == nil {
		return fmt.Errorf("%s succeeded, want a revert", what)
	}
	return nil
}

func (cc *chainClient) mintAndApprove(ctx context.Context, key *ecdsa.PrivateKey, quote, spender common.Address, amount int64) error {
	holder := crypto.PubkeyToAddress(key.PublicKey)
	if _, err := cc.sendTx(ctx, key, quote, packMint(holder, big.NewInt(amount))); err != nil {
		return fmt.Errorf("mint quote tokens: %w", err)
	}
	if _, err := cc.sendTx(ctx, key, quote, packApprove(spender, big.NewInt(amount))); err != nil {
		return fmt.Errorf("approve %s: %w", spender.Hex(), err)
	}
	return nil
}

// requestPurchase sends Vault.requestPurchase from key and returns the decoded PurchaseRequested log
func (cc *chainClient) requestPurchase(ctx context.Context, cfg config, key *ecdsa.PrivateKey, recipient common.Address, tokens *big.Int, deposit int64) (*types.Receipt, bindings.PurchaseRequestedEvent, error) {
	vault := bindings.NewVault()
	deadline, err := cc.deadline(ctx)
	if err != nil {
		return nil, bindings.PurchaseRequestedEvent{}, err
	}
	data, err := vault.PackRequestPurchase(tokens, big.NewInt(deposit), recipient, deadline)
	if err != nil {
		return nil, bindings.PurchaseRequestedEvent{}, err
	}
	receipt, err := cc.sendTx(ctx, key, cfg.vaultAddr, data)
	if err != nil {
		return nil, bindings.PurchaseRequestedEvent{}, fmt.Errorf("submit Vault.requestPurchase: %w", err)
	}
	for _, l := range receipt.Logs {
		if l.Address == cfg.vaultAddr && len(l.Topics) > 0 && l.Topics[0] == vault.EventID("PurchaseRequested") {
			ev, err := vault.UnpackPurchaseRequested(l.Data, l.Topics)
			return receipt, ev, err
		}
	}
	return nil, bindings.PurchaseRequestedEvent{}, fmt.Errorf("PurchaseRequested log not found in receipt")
}

// read model helpers

func getOrder(api *apiClient, id string) (*orderJSON, error) {
	var o orderJSON
	if err := api.getJSON("/api/v1/sales/orders/"+id, "", &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// expectFields compares got with want (name, got, want triples) and reports every difference
func expectFields(what string, fields ...[3]string) error {
	var bad []string
	for _, f := range fields {
		if !strings.EqualFold(f[1], f[2]) {
			bad = append(bad, fmt.Sprintf("%s = %q, want %q", f[0], f[1], f[2]))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s: %s", what, strings.Join(bad, "; "))
	}
	return nil
}

// waitOrder polls GET /sales/orders/{id} until the status is want, failing with the first
// difference found in check once it is
func waitOrder(api *apiClient, id, status string, check func(o *orderJSON) error) error {
	return poll("order "+id+" "+status, 90*time.Second, 3*time.Second, func() (bool, error) {
		o, err := getOrder(api, id)
		if err != nil {
			if apiErr, ok := err.(*apiError); ok && apiErr.status == http.StatusNotFound {
				return false, nil
			}
			return false, err
		}
		if o.Status != status {
			return false, nil
		}
		return true, check(o)
	})
}

// expectTxKinds polls GET /transactions for the address until every hash in want is listed,
// then requires its kind to match: the transaction index classifies the order transactions
func expectTxKinds(api *apiClient, address common.Address, want map[string]string) error {
	return poll("transaction kinds for "+address.Hex(), 90*time.Second, 3*time.Second, func() (bool, error) {
		var txs []struct{ TxHash, Kind string }
		if err := api.getJSON("/api/v1/transactions?limit=200&address="+address.Hex(), "", &txs); err != nil {
			return false, err
		}
		seen := map[string]string{}
		for _, t := range txs {
			seen[strings.ToLower(t.TxHash)] = t.Kind
		}
		for hash, kind := range want {
			got, ok := seen[strings.ToLower(hash)]
			if !ok {
				return false, nil
			}
			if got != kind {
				return false, fmt.Errorf("transaction %s kind = %q, want %q", hash, got, kind)
			}
		}
		return true, nil
	})
}

func forwardPurchase(ctx context.Context, api *apiClient, chain *chainClient, cfg config, adminKey, investorKey *ecdsa.PrivateKey, investor common.Address) error {
	vault := bindings.NewVault()
	strategy := bindings.NewOraclePriceStrategy()

	if err := chain.mintAndApprove(ctx, investorKey, cfg.quoteAddr, cfg.vaultAddr, purchaseDeposit); err != nil {
		return err
	}
	rwaBefore, err := chain.quoteBalance(ctx, cfg.tokenAddr, investor)
	if err != nil {
		return err
	}
	base, err := chain.feedRound(ctx, cfg.oracleFeed)
	if err != nil {
		return err
	}

	receipt, ev, err := chain.requestPurchase(ctx, cfg, investorKey, investor, amount10, purchaseDeposit)
	if err != nil {
		return err
	}
	id := ev.ID.String()
	createdAt, err := chain.blockTime(ctx, receipt)
	if err != nil {
		return err
	}
	if ev.Strategy != cfg.oracleStrategy || ev.BaseRound.Cmp(base) != 0 || ev.CreatedAt != createdAt {
		return fmt.Errorf("PurchaseRequested = strategy %s base round %s createdAt %d, want %s %s %d",
			ev.Strategy.Hex(), ev.BaseRound, ev.CreatedAt, cfg.oracleStrategy.Hex(), base, createdAt)
	}

	if err := waitOrder(api, id, "Pending", func(o *orderJSON) error {
		if err := expectFields("pending order",
			[3]string{"buyer", o.Buyer, investor.Hex()}, [3]string{"recipient", o.Recipient, investor.Hex()},
			[3]string{"tokenAmount", o.TokenAmount, amount10.String()}, [3]string{"maxQuoteAmount", o.MaxQuoteAmount, fmt.Sprint(purchaseDeposit)},
			[3]string{"strategy", o.Strategy, cfg.oracleStrategy.Hex()}, [3]string{"baseRound", o.BaseRound, base.String()},
			[3]string{"requestTxHash", o.RequestTxHash, receipt.TxHash.Hex()},
		); err != nil {
			return err
		}
		if o.CreatedAt != int64(createdAt) || o.TimeoutAt != int64(createdAt)+cfg.purchaseTimeout {
			return fmt.Errorf("pending order createdAt/timeoutAt = %d/%d, want %d/%d", o.CreatedAt, o.TimeoutAt, createdAt, int64(createdAt)+cfg.purchaseTimeout)
		}
		if o.PricedRound != "" || o.QuoteAmount != "" || o.Refund != "" || o.CloseTxHash != "" {
			return fmt.Errorf("pending order carries closing fields: %+v", o)
		}
		return nil
	}); err != nil {
		return err
	}
	var pending []orderJSON
	if err := api.getJSON("/api/v1/sales/orders?status=Pending&address="+strings.ToLower(investor.Hex()), "", &pending); err != nil {
		return err
	}
	if len(pending) != 1 || pending[0].ID != id {
		return fmt.Errorf("GET /sales/orders?status=Pending&address=<buyer> = %+v, want only order %s", pending, id)
	}
	if err := expectPendingDeposits(api, fmt.Sprint(purchaseDeposit)); err != nil {
		return err
	}

	// no round has landed after the order, so settling reverts
	settleData, err := vault.PackSettlePurchase(ev.ID)
	if err != nil {
		return err
	}
	if err := chain.expectRevert(ctx, investor, cfg.vaultAddr, settleData, "settlePurchase before a round lands"); err != nil {
		return err
	}

	if err := chain.publishAfterCutoff(ctx, cfg, adminKey, priceAfterPurchase); err != nil {
		return err
	}
	// the contract's own view of the pricing, against the literal worked out from the feed
	qdata, err := strategy.PackQuoteAfter("quotePurchaseAfter", amount10, base, createdAt)
	if err != nil {
		return err
	}
	qout, err := chain.call(ctx, cfg.oracleStrategy, qdata)
	if err != nil {
		return fmt.Errorf("quotePurchaseAfter: %w", err)
	}
	quote, err := strategy.UnpackQuoteAfter("quotePurchaseAfter", qout)
	if err != nil {
		return err
	}
	wantRound := new(big.Int).Add(base, big.NewInt(1))
	if quote.QuoteAmount.Cmp(big.NewInt(purchaseQuote)) != 0 || quote.RoundID.Cmp(wantRound) != 0 {
		return fmt.Errorf("quotePurchaseAfter = quote %s round %s, want %d at round %s", quote.QuoteAmount, quote.RoundID, purchaseQuote, wantRound)
	}

	// anyone can settle: the admin wallet does here, not the buyer
	settleReceipt, err := chain.sendTx(ctx, adminKey, cfg.vaultAddr, settleData)
	if err != nil {
		return fmt.Errorf("submit Vault.settlePurchase: %w", err)
	}
	if err := waitOrder(api, id, "Settled", func(o *orderJSON) error {
		return expectFields("settled order",
			[3]string{"pricedRound", o.PricedRound, wantRound.String()}, [3]string{"quoteAmount", o.QuoteAmount, fmt.Sprint(purchaseQuote)},
			[3]string{"refund", o.Refund, fmt.Sprint(purchaseRefund)}, [3]string{"closeTxHash", o.CloseTxHash, settleReceipt.TxHash.Hex()},
			[3]string{"baseRound", o.BaseRound, base.String()},
		)
	}); err != nil {
		return err
	}

	rwaAfter, err := chain.quoteBalance(ctx, cfg.tokenAddr, investor)
	if err != nil {
		return err
	}
	if got := new(big.Int).Sub(rwaAfter, rwaBefore); got.Cmp(amount10) != 0 {
		return fmt.Errorf("investor RWA balance rose by %s, want %s", got, amount10)
	}
	if err := expectPendingDeposits(api, "0"); err != nil {
		return err
	}
	if err := expectPurchaseRecorded(api, cfg, investor); err != nil {
		return err
	}
	if err := expectTxKinds(api, investor, map[string]string{receipt.TxHash.Hex(): "purchase_requested"}); err != nil {
		return err
	}
	// a settle that fills emits PurchaseSettled with Purchased and stays a purchase. The admin
	// signed it, so it is listed under the admin, not the buyer the events name
	return expectTxKinds(api, crypto.PubkeyToAddress(adminKey.PublicKey), map[string]string{settleReceipt.TxHash.Hex(): "purchase"})
}

// expectPurchaseRecorded requires the settled order's fill in the admin purchase history
func expectPurchaseRecorded(api *apiClient, cfg config, investor common.Address) error {
	return poll("settled order in the purchase history", 60*time.Second, 3*time.Second, func() (bool, error) {
		var purchases []struct{ Buyer, TokenAmount, QuoteAmount string }
		if err := api.getJSON("/api/v1/sales/purchases", cfg.adminBearer, &purchases); err != nil {
			return false, err
		}
		for _, p := range purchases {
			if strings.EqualFold(p.Buyer, investor.Hex()) && p.TokenAmount == amount10.String() && p.QuoteAmount == fmt.Sprint(purchaseQuote) {
				return true, nil
			}
		}
		return false, nil
	})
}

func getRedemptionJSON(api *apiClient, id string) (*redemptionJSON, error) {
	var r redemptionJSON
	if err := api.getJSON("/api/v1/redemptions/"+id, "", &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func forwardRedemption(ctx context.Context, api *apiClient, chain *chainClient, cfg config, treasuryKey *ecdsa.PrivateKey, treasurer common.Address, investorKey *ecdsa.PrivateKey, investor common.Address) error {
	escrow := bindings.NewRedemptionEscrow()
	const minQuoteOut = 1

	if _, err := chain.sendTx(ctx, investorKey, cfg.tokenAddr, packApprove(cfg.escrowAddr, amount5)); err != nil {
		return fmt.Errorf("approve escrow: %w", err)
	}
	base, err := chain.feedRound(ctx, cfg.oracleFeed)
	if err != nil {
		return err
	}
	deadline, err := chain.deadline(ctx)
	if err != nil {
		return err
	}
	data, err := escrow.PackRequestRedemption(amount5, big.NewInt(minQuoteOut), deadline)
	if err != nil {
		return err
	}
	receipt, err := chain.sendTx(ctx, investorKey, cfg.escrowAddr, data)
	if err != nil {
		return fmt.Errorf("submit RedemptionEscrow.requestRedemption: %w", err)
	}

	var requested *bindings.RedemptionRequestedEvent
	var forward *bindings.RedemptionForwardPricedEvent
	for _, l := range receipt.Logs {
		if l.Address != cfg.escrowAddr || len(l.Topics) == 0 {
			continue
		}
		switch l.Topics[0] {
		case escrow.EventID("RedemptionRequested"):
			ev, err := escrow.UnpackRedemptionRequested(l.Data, l.Topics)
			if err != nil {
				return err
			}
			requested = &ev
		case escrow.EventID("RedemptionForwardPriced"):
			ev, err := escrow.UnpackRedemptionForwardPriced(l.Data, l.Topics)
			if err != nil {
				return err
			}
			forward = &ev
		}
	}
	if requested == nil || forward == nil {
		return fmt.Errorf("receipt lacks RedemptionRequested (%v) or RedemptionForwardPriced (%v)", requested != nil, forward != nil)
	}
	if requested.QuoteAmount.Sign() != 0 {
		return fmt.Errorf("RedemptionRequested.quoteAmount = %s, want 0 on a forward request", requested.QuoteAmount)
	}
	if forward.Strategy != cfg.oracleStrategy || forward.BaseRound.Cmp(base) != 0 || forward.MinQuoteOut.Cmp(big.NewInt(minQuoteOut)) != 0 {
		return fmt.Errorf("RedemptionForwardPriced = strategy %s base %s min %s, want %s %s %d", forward.Strategy.Hex(), forward.BaseRound, forward.MinQuoteOut, cfg.oracleStrategy.Hex(), base, minQuoteOut)
	}
	id := requested.ID.String()

	if err := poll("forward redemption "+id+" Pending with a zero quote", 60*time.Second, 3*time.Second, func() (bool, error) {
		r, err := getRedemptionJSON(api, id)
		if err != nil {
			if apiErr, ok := err.(*apiError); ok && apiErr.status == http.StatusNotFound {
				return false, nil
			}
			return false, err
		}
		if r.Status != "Pending" {
			return false, nil
		}
		if err := expectFields("pending forward redemption",
			[3]string{"quoteAmount", r.QuoteAmount, "0"}, [3]string{"strategy", r.Strategy, cfg.oracleStrategy.Hex()},
			[3]string{"baseRound", r.BaseRound, base.String()}, [3]string{"minQuoteOut", r.MinQuoteOut, fmt.Sprint(minQuoteOut)},
			[3]string{"pricedRound", r.PricedRound, ""},
		); err != nil {
			return false, err
		}
		return true, nil
	}); err != nil {
		return err
	}

	// until a round lands after the cut-off, the quote cannot be previewed and funding cannot be priced
	previewData, err := escrow.PackPreviewFunding(requested.ID)
	if err != nil {
		return err
	}
	if err := chain.expectRevert(ctx, treasurer, cfg.escrowAddr, previewData, "previewFunding before a round lands"); err != nil {
		return err
	}

	if err := chain.publishAfterCutoff(ctx, cfg, treasuryKey, priceAfterRedemption); err != nil {
		return err
	}
	out, err := chain.call(ctx, cfg.escrowAddr, previewData)
	if err != nil {
		return fmt.Errorf("previewFunding: %w", err)
	}
	preview, err := escrow.UnpackPreviewFunding(out)
	if err != nil {
		return err
	}
	if preview.Cmp(big.NewInt(redemptionQuote)) != 0 {
		return fmt.Errorf("previewFunding = %s, want %d (5 RWA at 2.4 less the 1%% discount)", preview, redemptionQuote)
	}

	if err := chain.mintAndApprove(ctx, treasuryKey, cfg.quoteAddr, cfg.escrowAddr, redemptionQuote); err != nil {
		return err
	}
	fundData, err := escrow.PackFundRedemption(requested.ID)
	if err != nil {
		return err
	}
	fundReceipt, err := chain.sendTx(ctx, treasuryKey, cfg.escrowAddr, fundData)
	if err != nil {
		return fmt.Errorf("submit RedemptionEscrow.fundRedemption: %w", err)
	}
	// one more block so the confirmations reach the configured 1
	if err := chain.mineEmptyBlock(ctx); err != nil {
		return err
	}

	wantRound := new(big.Int).Add(base, big.NewInt(1))
	// the round is the one published after the cut-off, which is the feed's latest round now
	if latest, err := chain.feedRound(ctx, cfg.oracleFeed); err != nil || latest.Cmp(wantRound) != 0 {
		return fmt.Errorf("feed latest round = %v (err %v), want %s", latest, err, wantRound)
	}
	if err := poll("forward redemption "+id+" Funded, priced and claimable", 90*time.Second, 3*time.Second, func() (bool, error) {
		r, err := getRedemptionJSON(api, id)
		if err != nil {
			return false, err
		}
		if r.Status != "Funded" || !r.Claimable {
			return false, nil
		}
		return true, expectFields("funded forward redemption",
			[3]string{"quoteAmount", r.QuoteAmount, fmt.Sprint(redemptionQuote)}, [3]string{"pricedRound", r.PricedRound, wantRound.String()},
			[3]string{"baseRound", r.BaseRound, base.String()}, [3]string{"strategy", r.Strategy, cfg.oracleStrategy.Hex()},
		)
	}); err != nil {
		return err
	}

	quoteBefore, err := chain.quoteBalance(ctx, cfg.quoteAddr, investor)
	if err != nil {
		return err
	}
	claimData, err := escrow.PackClaimRedemption(requested.ID)
	if err != nil {
		return err
	}
	if _, err := chain.sendTx(ctx, investorKey, cfg.escrowAddr, claimData); err != nil {
		return fmt.Errorf("submit RedemptionEscrow.claimRedemption: %w", err)
	}
	if err := poll("forward redemption "+id+" Completed", 60*time.Second, 3*time.Second, func() (bool, error) {
		r, err := getRedemptionJSON(api, id)
		if err != nil {
			return false, err
		}
		return r.Status == "Completed" && r.QuoteAmount == fmt.Sprint(redemptionQuote), nil
	}); err != nil {
		return err
	}
	quoteAfter, err := chain.quoteBalance(ctx, cfg.quoteAddr, investor)
	if err != nil {
		return err
	}
	if got := new(big.Int).Sub(quoteAfter, quoteBefore); got.Cmp(big.NewInt(redemptionQuote)) != 0 {
		return fmt.Errorf("claim paid %s, want %d", got, redemptionQuote)
	}
	return expectTxKinds(api, treasurer, map[string]string{fundReceipt.TxHash.Hex(): "redemption_funded"})
}

func rejectedPurchase(ctx context.Context, api *apiClient, chain *chainClient, cfg config, adminKey, investorKey *ecdsa.PrivateKey, investor common.Address) error {
	vault := bindings.NewVault()

	if err := chain.mintAndApprove(ctx, investorKey, cfg.quoteAddr, cfg.vaultAddr, rejectedDeposit); err != nil {
		return err
	}
	quoteBefore, err := chain.quoteBalance(ctx, cfg.quoteAddr, investor)
	if err != nil {
		return err
	}
	receipt, ev, err := chain.requestPurchase(ctx, cfg, investorKey, investor, big.NewInt(1_000_000_000_000_000_000), rejectedDeposit)
	if err != nil {
		return err
	}
	id := ev.ID.String()
	if err := waitOrder(api, id, "Pending", func(*orderJSON) error { return nil }); err != nil {
		return err
	}

	// rejection needs a pause
	rejectData, err := vault.PackRejectPurchase(ev.ID)
	if err != nil {
		return err
	}
	if err := chain.expectRevert(ctx, crypto.PubkeyToAddress(adminKey.PublicKey), cfg.vaultAddr, rejectData, "rejectPurchase while not paused"); err != nil {
		return err
	}
	pause, err := pauseABI.Pack("pause")
	if err != nil {
		return err
	}
	if _, err := chain.sendTx(ctx, adminKey, cfg.tokenAddr, pause); err != nil {
		return fmt.Errorf("pause: %w", err)
	}
	rejectReceipt, err := chain.sendTx(ctx, adminKey, cfg.vaultAddr, rejectData)
	if err != nil {
		return fmt.Errorf("submit Vault.rejectPurchase: %w", err)
	}
	unpause, err := pauseABI.Pack("unpause")
	if err != nil {
		return err
	}
	if _, err := chain.sendTx(ctx, adminKey, cfg.tokenAddr, unpause); err != nil {
		return fmt.Errorf("unpause: %w", err)
	}

	if err := waitOrder(api, id, "Rejected", func(o *orderJSON) error {
		if err := expectFields("rejected order",
			[3]string{"refund", o.Refund, fmt.Sprint(rejectedDeposit)}, [3]string{"closeTxHash", o.CloseTxHash, rejectReceipt.TxHash.Hex()},
			[3]string{"requestTxHash", o.RequestTxHash, receipt.TxHash.Hex()},
		); err != nil {
			return err
		}
		if o.PricedRound != "" || o.QuoteAmount != "" {
			return fmt.Errorf("a rejected order was never priced, got round %q quote %q", o.PricedRound, o.QuoteAmount)
		}
		return nil
	}); err != nil {
		return err
	}
	quoteAfter, err := chain.quoteBalance(ctx, cfg.quoteAddr, investor)
	if err != nil {
		return err
	}
	if quoteAfter.Cmp(quoteBefore) != 0 {
		return fmt.Errorf("investor quote balance %s after the rejection, want %s: the whole deposit comes back", quoteAfter, quoteBefore)
	}
	if err := expectPendingDeposits(api, "0"); err != nil {
		return err
	}
	if err := expectTxKinds(api, investor, map[string]string{receipt.TxHash.Hex(): "purchase_requested"}); err != nil {
		return err
	}
	// the treasurer (here the admin wallet) signed the rejection, so it is listed under that wallet
	return expectTxKinds(api, crypto.PubkeyToAddress(adminKey.PublicKey), map[string]string{rejectReceipt.TxHash.Hex(): "purchase_rejected"})
}

// keeperPurchase runs the keeper with its own config and wallet. Before the round exists the
// order must stay untouched and the wallet must send nothing; after it, one transaction settles
// the order at the published price.
func keeperPurchase(ctx context.Context, api *apiClient, chain *chainClient, cfg config, adminKey, investorKey *ecdsa.PrivateKey, investor common.Address) error {
	key, err := crypto.HexToECDSA(strings.TrimPrefix(cfg.keeperPK, "0x"))
	if err != nil {
		return fmt.Errorf("KEEPER_PK: %w", err)
	}
	wallet := crypto.PubkeyToAddress(key.PublicKey)
	configPath := filepath.Join(cfg.workDir, "keeper.yaml")
	body := fmt.Sprintf("rpc_url: %q\nchain_id: %s\nvault_address: %q\nkeys:\n  keeper_key: %q\n",
		cfg.rpcURL, cfg.chainID, cfg.vaultAddr.Hex(), cfg.keeperPK)
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		return err
	}
	runKeeper := func(wantNonce uint64) error {
		if out, err := exec.CommandContext(ctx, cfg.keeperBin, "--config", configPath, "--once").CombinedOutput(); err != nil {
			return fmt.Errorf("keeper --once: %w\n%s", err, out)
		}
		nonce, err := chain.eth.PendingNonceAt(ctx, wallet)
		if err != nil {
			return err
		}
		if nonce != wantNonce {
			return fmt.Errorf("keeper wallet nonce = %d, want %d", nonce, wantNonce)
		}
		return nil
	}

	if err := chain.mintAndApprove(ctx, investorKey, cfg.quoteAddr, cfg.vaultAddr, keeperDeposit); err != nil {
		return err
	}
	_, ev, err := chain.requestPurchase(ctx, cfg, investorKey, investor, big.NewInt(1_000_000_000_000_000_000), keeperDeposit)
	if err != nil {
		return err
	}
	id := ev.ID.String()
	if err := waitOrder(api, id, "Pending", func(*orderJSON) error { return nil }); err != nil {
		return err
	}
	if err := runKeeper(0); err != nil {
		return fmt.Errorf("before the round: %w", err)
	}

	if err := chain.publishAfterCutoff(ctx, cfg, adminKey, priceAfterPurchase); err != nil {
		return err
	}
	if err := runKeeper(1); err != nil {
		return fmt.Errorf("after the round: %w", err)
	}
	if err := waitOrder(api, id, "Settled", func(o *orderJSON) error {
		return expectFields("keeper order",
			[3]string{"quoteAmount", o.QuoteAmount, fmt.Sprint(keeperQuote)},
			[3]string{"refund", o.Refund, fmt.Sprint(keeperDeposit - keeperQuote)})
	}); err != nil {
		return err
	}
	return expectPendingDeposits(api, "0")
}
