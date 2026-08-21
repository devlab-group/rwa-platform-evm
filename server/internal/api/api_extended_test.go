package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/auth"
	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/compliance"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/kyc"
	"github.com/rwa-platform/server/internal/txindex"
)

func sign(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func doJSONRaw(t *testing.T, router *gin.Engine, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestVerifyChallengeEndToEnd(t *testing.T) {
	env := setupTestApp(t)
	priv, _ := crypto.GenerateKey()
	walletAddr := crypto.PubkeyToAddress(priv.PublicKey)

	w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/challenge", map[string]string{"address": walletAddr.Hex()}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("createChallenge status = %d, body=%s", w.Code, w.Body.String())
	}
	var ch dto.ChallengeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &ch); err != nil {
		t.Fatal(err)
	}

	hash := accounts.TextHash([]byte(ch.Message))
	sig, err := crypto.Sign(hash, priv)
	if err != nil {
		t.Fatal(err)
	}

	verifyBody := ChallengeVerifyBody{Address: walletAddr.Hex(), Nonce: ch.Nonce, Signature: "0x" + common.Bytes2Hex(sig)}
	w = doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/challenge/verify", verifyBody, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("verifyChallenge status = %d, body=%s", w.Code, w.Body.String())
	}
	var status dto.VerifyChallengeResult
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.OwnershipVerified {
		t.Error("expected OwnershipVerified = true")
	}
	if status.Address != walletAddr.Hex() {
		t.Errorf("Address = %s", status.Address)
	}

	// Proving ownership must mint a subject-scoped session token, and
	// that token must authenticate GET /me/wallet-status for THIS address
	// with no admin credential.
	if status.SessionToken == "" {
		t.Fatal("expected verifyChallenge to mint a SessionToken")
	}
	if status.SessionExpiresAt == "" {
		t.Error("expected a SessionExpiresAt alongside the token")
	}
	ws := doJSON(t, env.router, http.MethodGet, "/api/v1/me/wallet-status", nil,
		map[string]string{auth.WalletSessionHeader: status.SessionToken})
	if ws.Code != http.StatusOK {
		t.Fatalf("wallet-status with minted session = %d, body=%s", ws.Code, ws.Body.String())
	}
	var self dto.WalletStatus
	if err := json.Unmarshal(ws.Body.Bytes(), &self); err != nil {
		t.Fatal(err)
	}
	if self.Address != walletAddr.Hex() {
		t.Errorf("wallet-status Address = %s, want %s", self.Address, walletAddr.Hex())
	}
	if !self.OwnershipVerified {
		t.Error("wallet-status OwnershipVerified = false, want true")
	}

	// Replaying the same challenge must fail.
	w = doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/challenge/verify", verifyBody, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("replay status = %d, want 400", w.Code)
	}
}

func TestVerifyChallengeRejectsWrongAddress(t *testing.T) {
	env := setupTestApp(t)
	priv, _ := crypto.GenerateKey()
	walletAddr := crypto.PubkeyToAddress(priv.PublicKey)

	w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/challenge", map[string]string{"address": walletAddr.Hex()}, nil)
	var ch dto.ChallengeResponse
	_ = json.Unmarshal(w.Body.Bytes(), &ch)

	hash := accounts.TextHash([]byte(ch.Message))
	sig, _ := crypto.Sign(hash, priv)

	verifyBody := ChallengeVerifyBody{Address: addr("0xdead"), Nonce: ch.Nonce, Signature: "0x" + common.Bytes2Hex(sig)}
	w = doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/challenge/verify", verifyBody, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for mismatched address", w.Code)
	}
}

// TestCreateChallengeEnforcesActiveCapWith429 is the HTTP-level half of the
// active-cap behavior: once an address has too many active challenges
// outstanding, POST /compliance/challenge must reject with 429 and the stable
// CodeTooManyActiveChallenges, not a generic 400.
func TestCreateChallengeEnforcesActiveCapWith429(t *testing.T) {
	env := setupTestApp(t)
	env.app.Challenges.MaxActiveChallengesPerAddress = 2
	priv, _ := crypto.GenerateKey()
	walletAddr := crypto.PubkeyToAddress(priv.PublicKey)

	for i := 0; i < 2; i++ {
		w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/challenge", map[string]string{"address": walletAddr.Hex()}, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("create #%d: status = %d, body=%s", i, w.Code, w.Body.String())
		}
	}

	w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/challenge", map[string]string{"address": walletAddr.Hex()}, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429, body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != CodeTooManyActiveChallenges {
		t.Errorf("code = %v, want %s", body["code"], CodeTooManyActiveChallenges)
	}
}

// A provider delivery that is correctly signed and carries a real decision, but
// no provider timestamp, must be DROPPED rather than applied with a fabricated
// one (internal/compliance derives both the freshness window and the
// newest-decision-wins ordering from it — see kyc.ErrMissingTimestamp).
//
// The HTTP answer is 202, not a 4xx: the condition is permanent, and both
// providers retry non-2xx — Sumsub auto-disabling the webhook after continuous
// failures — so a refusal here would risk blocking every later, well-formed
// delivery. The drop is made visible through the audit log instead, which is
// what this test pins: acknowledged, nothing applied, and an operator-visible
// record that it happened.
func TestKYCWebhookDropsDecisionWithNoProviderTimestamp(t *testing.T) {
	env := setupTestApp(t)
	walletAddr := addr("0xB0B0")
	_ = env.app.Repos.Investors.Upsert(context.Background(), &models.Investor{Address: walletAddr, OwnershipVerified: true})

	// Swap the generic provider for Onfido, whose webhook shape carries the
	// decision timestamp in the payload rather than taking it on trust.
	onfido, err := kyc.New(kyc.Config{
		Mode:   kyc.ModeOnfido,
		Onfido: kyc.OnfidoConfig{APIToken: "tok", WebhookToken: "whtok", WorkflowID: "wf1", Region: "eu"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env.app.KYC = onfido

	// Bind the workflow run to the wallet FIRST. Without this the handler
	// returns its own 422 for an unresolvable subject reference, and this test
	// would pass whether or not the timestamp is checked at all.
	if err := env.app.Repos.KYCVerifications.Upsert(context.Background(), &models.KYCVerification{
		Provider: "onfido", Ref: "run-123", Address: walletAddr,
	}); err != nil {
		t.Fatal(err)
	}

	// An approved workflow run with no completed_at_iso8601.
	payload := []byte(`{"payload":{"resource_type":"workflow_run","action":"workflow_run.completed","object":{"id":"run-123","status":"approved"}}}`)
	w := doJSONRaw(t, env.router, http.MethodPost, "/api/v1/compliance/webhook", payload,
		map[string]string{"X-SHA2-Signature": sign(payload, "whtok")})
	// Acknowledged so the provider does not retry a permanently-unprocessable
	// delivery (and, for Sumsub, does not count it toward auto-disablement).
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", w.Code, w.Body.String())
	}

	// ...but NOTHING may have been applied: no decision recorded, so the
	// fabricated-timestamp path is genuinely gone rather than merely renamed.
	events, err := env.app.Repos.KYCEvents.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expected no recorded kyc events, got %d: %+v", len(events), events)
	}

	// The 202 must not make the drop invisible — that is the whole reason this
	// is not simply folded into the ErrUnhandledEvent branch. An operator has
	// to be able to find it (GET /audit-logs) and apply the decision by hand.
	logs, err := env.app.Repos.AuditLogs.List(context.Background(), "compliance", 50)
	if err != nil {
		t.Fatal(err)
	}
	var dropped bool
	for _, e := range logs {
		if e.Action == "compliance.webhookDroppedNoTimestamp" {
			dropped = true
		}
		if e.Action == "compliance.webhookAccepted" {
			t.Fatalf("a timestamp-less delivery must not be audited as accepted: %+v", e)
		}
	}
	if !dropped {
		t.Fatalf("dropped decision left no audit trail; entries=%+v", logs)
	}

	// The same delivery WITH the provider's timestamp is accepted, so the
	// rejection above is specifically about the missing timestamp and nothing
	// else in the payload.
	ok := []byte(`{"payload":{"resource_type":"workflow_run","action":"workflow_run.completed","object":{"id":"run-123","status":"approved","completed_at_iso8601":"` +
		time.Now().UTC().Format(time.RFC3339) + `"}}}`)
	w2 := doJSONRaw(t, env.router, http.MethodPost, "/api/v1/compliance/webhook", ok,
		map[string]string{"X-SHA2-Signature": sign(ok, "whtok")})
	if w2.Code != http.StatusAccepted {
		t.Fatalf("timestamped delivery: status = %d, want 202, body=%s", w2.Code, w2.Body.String())
	}
}

func TestKYCWebhookRecordsWebhookEventAndListsIt(t *testing.T) {
	env := setupTestApp(t)
	walletAddr := addr("0xB0B0")
	_ = env.app.Repos.Investors.Upsert(context.Background(), &models.Investor{Address: walletAddr, OwnershipVerified: true})

	payload := []byte(`{"eventId":"evt-1","provider":"test-provider","address":"` + walletAddr + `","status":"Allowed","occurredAt":` + strconv.FormatInt(time.Now().UTC().Unix(), 10) + `}`)
	sig := sign(payload, "webhook-secret")

	req := doJSONRaw(t, env.router, http.MethodPost, "/api/v1/compliance/webhook", payload, map[string]string{"X-Webhook-Signature": sig})
	if req.Code != http.StatusAccepted {
		t.Fatalf("webhook status = %d, body=%s", req.Code, req.Body.String())
	}

	// The handler only durably accepts the decision — submitting the
	// on-chain status tx and confirming it happens asynchronously via
	// WebhookReconciler, exactly as
	// cmd/platform/main.go's background loop drives it. Replicate one
	// submit tick + one confirm tick here rather than asserting Applied
	// immediately after the webhook POST.
	ctx := context.Background()
	reconciler := compliance.NewWebhookReconciler(env.app.Repos.KYCEvents, env.app.Repos.Transactions, env.app.Status)
	if err := reconciler.Reconcile(ctx); err != nil {
		t.Fatalf("reconciler.Reconcile (submit): %v", err)
	}
	pendingTxs, err := env.app.Repos.Transactions.List(ctx)
	if err != nil || len(pendingTxs) != 1 {
		t.Fatalf("expected 1 submitted transaction, got %d (err %v)", len(pendingTxs), err)
	}
	env.client.SetReceipt(common.HexToHash(pendingTxs[0].TxHash), &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(1)})
	txs := blockchain.NewTxManager(env.client, env.app.Repos.Transactions, big.NewInt(31337), blockchain.FeeModeLegacy)
	if err := txs.RefreshStatuses(ctx, 0); err != nil {
		t.Fatalf("RefreshStatuses: %v", err)
	}
	if err := reconciler.Reconcile(ctx); err != nil {
		t.Fatalf("reconciler.Reconcile (confirm): %v", err)
	}

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/compliance/webhooks", nil, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("listWebhookEvents status = %d", w.Code)
	}
	var events []dto.WebhookEventResponse
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 webhook event, got %d", len(events))
	}
	if events[0].Outcome != "Allowed" || !events[0].Applied || events[0].ApplyStatus != "Applied" {
		t.Errorf("got %+v", events[0])
	}
}

func TestListAuditLogsFiltersByCategory(t *testing.T) {
	env := setupTestApp(t)

	// Generate a compliance-category log entry via setComplianceStatus.
	body := SetStatusRequestBody{Address: addr("0xC0C0"), Status: "Allowed"}
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/status", body, map[string]string{"Authorization": env.bearer, "Idempotency-Key": "audit-log-category-status"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("setComplianceStatus status = %d, body=%s", w.Code, w.Body.String())
	}

	w = doJSON(t, env.router, http.MethodGet, "/api/v1/audit-logs?category=compliance", nil, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var logs []dto.AuditLogResponse
	if err := json.Unmarshal(w.Body.Bytes(), &logs); err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("expected at least 1 compliance audit log entry")
	}
	for _, l := range logs {
		if l.Category != "compliance" {
			t.Errorf("got category %q, want compliance", l.Category)
		}
	}

	w = doJSON(t, env.router, http.MethodGet, "/api/v1/audit-logs?category=assets", nil, map[string]string{"Authorization": env.bearer})
	var noneLogs []dto.AuditLogResponse
	_ = json.Unmarshal(w.Body.Bytes(), &noneLogs)
	if len(noneLogs) != 0 {
		t.Errorf("expected 0 assets-category logs, got %d", len(noneLogs))
	}
}

func TestListPurchasesEmpty(t *testing.T) {
	env := setupTestApp(t)
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/sales/purchases", nil, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var purchases []any
	_ = json.Unmarshal(w.Body.Bytes(), &purchases)
	if len(purchases) != 0 {
		t.Errorf("expected empty, got %d", len(purchases))
	}
}

// TestListPurchasesPaginatesViaKeysetCursor is the API-level test for keyset
// pagination: with more than one page's worth of purchases, the
// response is capped at `limit`, X-Next-Cursor is present, and walking it
// via the `cursor` query parameter visits every purchase exactly once with
// no gaps or duplicates — proving the handler end to end, not just the
// repository layer (internal/dal/memory's pagination_test.go
// covers that).
func TestListPurchasesPaginatesViaKeysetCursor(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	const total = 55
	for i := 0; i < total; i++ {
		txHash := fmt.Sprintf("0xtx%04d", i)
		if err := env.app.Repos.Purchases.Create(ctx, &models.Purchase{
			ID: txHash + ":0", TxHash: txHash, BlockNumber: uint64(i), Buyer: "0xBUYER",
		}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination walk did not terminate")
		}
		url := "/api/v1/sales/purchases?limit=20"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		w := doJSON(t, env.router, http.MethodGet, url, nil, map[string]string{"Authorization": env.bearer})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		var page []struct {
			TxHash string `json:"txHash"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, p := range page {
			if seen[p.TxHash] {
				t.Fatalf("purchase %s returned twice across pages", p.TxHash)
			}
			seen[p.TxHash] = true
		}
		next := w.Header().Get("X-Next-Cursor")
		if w.Header().Get("X-Total-Count") != "" {
			t.Error("expected X-Total-Count to be omitted for a keyset-paginated endpoint")
		}
		if next == "" {
			if len(page) == 0 && pages == 0 {
				t.Fatal("expected at least one page")
			}
			break
		}
		cursor = next
	}

	if len(seen) != total {
		t.Fatalf("visited %d of %d purchases across all pages", len(seen), total)
	}
}

// TestListWalletsPaginatesViaKeysetCursor is the API-level test
// for InvestorRepository.ListPage — mirrors
// TestListPurchasesPaginatesViaKeysetCursor's structure.
func TestListWalletsPaginatesViaKeysetCursor(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	const total = 55
	for i := 0; i < total; i++ {
		addr := common.BigToAddress(big.NewInt(int64(1000 + i))).Hex()
		if err := env.app.Repos.Investors.Upsert(ctx, &models.Investor{
			Address: addr, Status: models.ComplianceAllowed,
		}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination walk did not terminate")
		}
		url := "/api/v1/compliance/wallets?limit=20"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		w := doJSON(t, env.router, http.MethodGet, url, nil, map[string]string{"Authorization": env.bearer})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		var page []dto.WalletStatus
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, inv := range page {
			if seen[inv.Address] {
				t.Fatalf("wallet %s returned twice across pages", inv.Address)
			}
			seen[inv.Address] = true
		}
		next := w.Header().Get("X-Next-Cursor")
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != total {
		t.Fatalf("visited %d of %d wallets across all pages", len(seen), total)
	}
}

func TestRedemptionResponseIncludesClaimable(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	// Funded 5 blocks before "now" (LastIndexedBlock=100), finality=3 -> claimable.
	if err := env.app.Repos.RedemptionRequests.Upsert(ctx, &models.RedemptionRequest{
		ID: "1", Beneficiary: addr("0xB0B0"), RWAAmount: "1000", QuoteAmount: "950",
		Status: models.RedemptionFunded, FundedAtBlock: 95, CreatedAt: 1000, TimeoutAt: 2000,
	}); err != nil {
		t.Fatal(err)
	}

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/redemptions/1", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var r dto.RedemptionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Claimable {
		t.Errorf("expected claimable=true, got %+v", r)
	}
	if r.Confirmations != 5 {
		t.Errorf("Confirmations = %d, want 5", r.Confirmations)
	}
}

func TestRedemptionResponseReflectsLiveBeneficiaryAllowed(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	beneficiary := addr("0xB0B1")
	if err := env.app.Repos.RedemptionRequests.Upsert(ctx, &models.RedemptionRequest{
		ID: "2", Beneficiary: beneficiary, RWAAmount: "1000", QuoteAmount: "950",
		Status: models.RedemptionPending, CreatedAt: 1000, TimeoutAt: 2000,
	}); err != nil {
		t.Fatal(err)
	}

	// Can the fake ComplianceRegistry to return isAllowed=true for this beneficiary.
	registry := bindings.NewComplianceRegistry()
	data, err := registry.PackIsAllowed(common.HexToAddress(beneficiary))
	if err != nil {
		t.Fatal(err)
	}
	ret, err := registry.ABI.Methods["isAllowed"].Outputs.Pack(true)
	if err != nil {
		t.Fatal(err)
	}
	env.client.CallResponses[common.Bytes2Hex(data)] = ret

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/redemptions/2", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var r dto.RedemptionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if !r.BeneficiaryAllowed {
		t.Errorf("expected BeneficiaryAllowed=true, got %+v", r)
	}
}

func TestListTransactionsFiltersByAddress(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = env.app.Repos.Transactions.Create(ctx, &models.Transaction{
		ID: "tx1", Kind: "compliance.setStatus", From: addr("0xAAAA"), To: addr("0xBBBB"),
		TxHash: "0x01", Status: models.TxPending, SubmittedAt: now, UpdatedAt: now,
	})
	_ = env.app.Repos.Transactions.Create(ctx, &models.Transaction{
		ID: "tx2", Kind: "assets.relaySignedResult", From: addr("0xCCCC"), To: addr("0xDDDD"),
		TxHash: "0x02", Status: models.TxPending, SubmittedAt: now, UpdatedAt: now,
	})

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/transactions?address="+addr("0xAAAA"), nil, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var txs []dto.TransactionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &txs); err != nil {
		t.Fatal(err)
	}
	if len(txs) != 1 || txs[0].TxHash != "0x01" {
		t.Fatalf("got %+v", txs)
	}

	w = doJSON(t, env.router, http.MethodGet, "/api/v1/transactions", nil, map[string]string{"Authorization": env.bearer})
	var all []dto.TransactionResponse
	_ = json.Unmarshal(w.Body.Bytes(), &all)
	if len(all) != 2 {
		t.Errorf("expected 2 unfiltered transactions, got %d", len(all))
	}
}

// TestListTransactionsIncludesEventDerived: after the stack-transactions
// projector runs, a wallet-broadcast on-chain action (here a treasury
// withdrawal) that never touched TxManager appears in GET /transactions, and
// the ?address= filter finds it by the acting EOA (caller).
func TestListTransactionsIncludesEventDerived(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	vault := addr("0x0000000000000000000000000000000000000004")
	caller := addr("0xCAFE")
	if err := env.app.Repos.ChainEvents.Create(ctx, &models.ChainEvent{
		ChainID: env.app.ChainID, Address: vault, TxHash: "0xwd1", LogIndex: 0, BlockNumber: 10,
		BlockHash: "0xblk", Name: "ProceedsWithdrawn", IndexedAt: time.Now().UTC(),
		Data: map[string]any{"treasury": addr("0xBEEF"), "quoteAmount": "4200", "caller": caller},
	}); err != nil {
		t.Fatal(err)
	}
	if err := txindex.ReconcileStackTransactions(ctx, env.app.Repos.Transactions, env.app.Repos.ChainEvents, env.app.Repos.IndexerCheckpoints, env.app.ChainID); err != nil {
		t.Fatalf("ReconcileStackTransactions: %v", err)
	}

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/transactions?address="+caller, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var txs []dto.TransactionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &txs); err != nil {
		t.Fatal(err)
	}
	if len(txs) != 1 || txs[0].TxHash != "0xwd1" || txs[0].Kind != "treasury_withdrawal" || txs[0].Sender != caller {
		t.Fatalf("event-derived tx not surfaced/filtered correctly: %+v", txs)
	}
	if txs[0].Status != string(models.TxConfirmed) {
		t.Errorf("Status = %q, want confirmed", txs[0].Status)
	}
}

// TestListRedemptionsFiltersByBeneficiary covers the public investor-UI
// endpoint: GET /redemptions?address=<b> returns only that beneficiary's
// requests (case-insensitively) and is reachable with NO bearer token.
func TestListRedemptionsFiltersByBeneficiary(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	alice := addr("0xAAAA")
	bob := addr("0xBBBB")
	_ = env.app.Repos.RedemptionRequests.Upsert(ctx, &models.RedemptionRequest{
		ID: "1", Beneficiary: alice, RWAAmount: "100", QuoteAmount: "95",
		Status: models.RedemptionPending, CreatedAt: 1, UpdatedAt: now,
	})
	_ = env.app.Repos.RedemptionRequests.Upsert(ctx, &models.RedemptionRequest{
		ID: "2", Beneficiary: bob, RWAAmount: "200", QuoteAmount: "190",
		Status: models.RedemptionPending, CreatedAt: 2, UpdatedAt: now,
	})

	// Public: no Authorization header. Address filter is case-insensitive.
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/redemptions?address="+strings.ToLower(alice), nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got []dto.RedemptionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "1" || got[0].Beneficiary != alice {
		t.Fatalf("address filter got %+v, want only alice's request", got)
	}

	// Unfiltered (also public) returns both.
	w = doJSON(t, env.router, http.MethodGet, "/api/v1/redemptions", nil, nil)
	var all []dto.RedemptionResponse
	_ = json.Unmarshal(w.Body.Bytes(), &all)
	if len(all) != 2 {
		t.Errorf("expected 2 unfiltered redemptions, got %d", len(all))
	}
}

// TestListTransactionsPaginatesViaKeysetCursor is the API-level test
// for TransactionRepository.ListPage — mirrors
// TestListPurchasesPaginatesViaKeysetCursor's structure.
func TestListTransactionsPaginatesViaKeysetCursor(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	const total = 55
	now := time.Now().UTC()
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("tx-%04d", i)
		if err := env.app.Repos.Transactions.Create(ctx, &models.Transaction{
			ID: id, Kind: "test", From: addr("0xAAAA"), To: addr("0xBBBB"),
			TxHash: fmt.Sprintf("0x%04x", i), Status: models.TxPending,
			SubmittedAt: now.Add(time.Duration(i) * time.Millisecond), UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination walk did not terminate")
		}
		url := "/api/v1/transactions?limit=20"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		w := doJSON(t, env.router, http.MethodGet, url, nil, map[string]string{"Authorization": env.bearer})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		var page []dto.TransactionResponse
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, tx := range page {
			if seen[tx.TxHash] {
				t.Fatalf("transaction %s returned twice across pages", tx.TxHash)
			}
			seen[tx.TxHash] = true
		}
		if w.Header().Get("X-Total-Count") != "" {
			t.Error("expected X-Total-Count to be omitted for a keyset-paginated endpoint")
		}
		next := w.Header().Get("X-Next-Cursor")
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != total {
		t.Fatalf("visited %d of %d transactions across all pages", len(seen), total)
	}
}

func TestProjectResponseIncludesExtendedFields(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	p, err := env.app.Repos.Projects.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.TokenUnit = "gram"
	p.RedemptionManager = addr("0xEEEE")
	p.BytecodeVerified = true
	p.Roles = map[string][]string{"PAUSER_ROLE": {addr("0xFFFF")}}
	p.Addresses.QuoteToken = addr("0x1234")
	p.QuoteDecimals = 6
	if err := env.app.Repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/project", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp dto.ProjectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TokenUnit != "gram" {
		t.Errorf("TokenUnit = %q", resp.TokenUnit)
	}
	// FinalityConfirmations reflects the live server config (set to 3 in
	// setupTestApp), not whatever might be persisted on the project record —
	// see (*App).toProjectResponse.
	if resp.FinalityConfirmations != 3 {
		t.Errorf("FinalityConfirmations = %d, want 3 (from app.FinalityConfirmations)", resp.FinalityConfirmations)
	}
	if !resp.BytecodeVerified {
		t.Error("expected BytecodeVerified = true")
	}
	if resp.Addresses.QuoteToken != addr("0x1234") {
		t.Errorf("QuoteToken = %s", resp.Addresses.QuoteToken)
	}
	if resp.QuoteDecimals != 6 {
		t.Errorf("QuoteDecimals = %d, want 6", resp.QuoteDecimals)
	}
	if len(resp.Roles["PAUSER_ROLE"]) != 1 {
		t.Errorf("Roles = %+v", resp.Roles)
	}
}

// TestProjectResponsePriceOverlay confirms GET /project exposes the live
// prices, preferring the event-sourced projection over the deploy baseline.
func TestProjectResponsePriceOverlay(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	p, err := env.app.Repos.Projects.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Deploy-baseline prices only (no projection yet) -> baseline is shown.
	p.PurchasePricePerWholeToken = "1000"
	p.RedemptionPricePerWholeToken = "900"
	p.Security = nil
	if err := env.app.Repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	resp := getProjectResp(t, env)
	if resp.PurchasePricePerWholeToken != "1000" || resp.RedemptionPricePerWholeToken != "900" {
		t.Fatalf("baseline prices = %s/%s, want 1000/900", resp.PurchasePricePerWholeToken, resp.RedemptionPricePerWholeToken)
	}

	// A live projection overlays the baseline.
	p.Security = &models.SecurityState{
		PurchasePricePerWholeToken:   "1500",
		RedemptionPricePerWholeToken: "1400",
	}
	if err := env.app.Repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	resp = getProjectResp(t, env)
	if resp.PurchasePricePerWholeToken != "1500" || resp.RedemptionPricePerWholeToken != "1400" {
		t.Fatalf("live prices = %s/%s, want the projected 1500/1400", resp.PurchasePricePerWholeToken, resp.RedemptionPricePerWholeToken)
	}
}

// TestProjectResponseStrategyOverlay: addresses.strategy is what the admin
// console targets when it sets a price or grants PRICER_ROLE, so after a
// Vault.setStrategy it must report the live strategy, not the deploy
// baseline — otherwise those transactions go to the contract the Vault
// stopped pricing through.
func TestProjectResponseStrategyOverlay(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	p, err := env.app.Repos.Projects.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deployed := "0x000000000000000000000000000000000000A11A"
	p.Addresses.Strategy = deployed

	// No projection yet: the deploy baseline is what there is.
	p.Security = nil
	if err := env.app.Repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := getProjectResp(t, env).Addresses.Strategy; got != deployed {
		t.Fatalf("strategy = %s with no projection, want the baseline %s", got, deployed)
	}

	// The Vault has since been repointed.
	swapped := "0x000000000000000000000000000000000000B11B"
	p.Security = &models.SecurityState{Strategy: swapped}
	if err := env.app.Repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := getProjectResp(t, env).Addresses.Strategy; got != swapped {
		t.Fatalf("strategy = %s after a swap, want the live %s", got, swapped)
	}
}

// TestProjectResponseLifecycleFields confirms GET /project surfaces the
// deployment lifecycle status and verification note the admin UI polls.
func TestProjectResponseLifecycleFields(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	p, err := env.app.Repos.Projects.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// A deployment still in progress reports its status.
	p.Status = models.ProjectStatusDeploying
	p.VerificationNote = ""
	if err := env.app.Repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	resp := getProjectResp(t, env)
	if resp.Status != "Deploying" {
		t.Fatalf("status = %q, want Deploying", resp.Status)
	}
	if resp.VerificationNote != "" {
		t.Fatalf("verificationNote = %q, want empty", resp.VerificationNote)
	}

	// A failed deployment surfaces the recorded reason.
	p.Status = models.ProjectStatusFailed
	p.VerificationNote = "missing required roles: [PAUSER_ROLE on token]"
	if err := env.app.Repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	resp = getProjectResp(t, env)
	if resp.Status != "Failed" {
		t.Fatalf("status = %q, want Failed", resp.Status)
	}
	if resp.VerificationNote != "missing required roles: [PAUSER_ROLE on token]" {
		t.Fatalf("verificationNote = %q", resp.VerificationNote)
	}
}

func getProjectResp(t *testing.T, env *testEnv) dto.ProjectResponse {
	t.Helper()
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/project", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp dto.ProjectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}
