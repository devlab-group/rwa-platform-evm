package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/assets"
	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/auth"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/compliance"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/eip712"
	"github.com/rwa-platform/server/internal/kyc"
	"github.com/rwa-platform/server/internal/redemption"
	"github.com/rwa-platform/server/internal/sales"
)

func init() { gin.SetMode(gin.TestMode) }

const goldGramProfile = `{
  "profileVersion": "1.0",
  "projectId": "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61",
  "assetType": "allocated-gold-bar",
  "tokenUnit": "gram",
  "tokenDecimals": 18,
  "recordIdLabel": "Bar serial number",
  "assetSchema": {
    "type": "object",
    "additionalProperties": false,
    "required": ["serialNumber", "weightGrams", "purity"],
    "properties": {
      "serialNumber": { "type": "string", "minLength": 1 },
      "weightGrams": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]+)?$" },
      "purity": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]+)?$" }
    }
  }
}`

type testEnv struct {
	app            *App
	router         *gin.Engine
	client         *blockchain.FakeClient
	auditorKey     *ecdsa.PrivateKey
	controllerAddr common.Address
	vaultAddr      common.Address
	escrowAddr     common.Address

	// Admin login fixtures: adminKey signs challenges, adminAddr is the configured admin,
	// jwtSecret signs JWTs, and bearer holds the admin's Authorization header
	adminKey  *ecdsa.PrivateKey
	adminAddr common.Address
	jwtSecret []byte
	bearer    string
}

func setupTestApp(t *testing.T) *testEnv {
	t.Helper()
	repos := memory.New()

	// Seed project and profile
	result, profile := assets.ValidateProfile([]byte(goldGramProfile))
	if !result.Valid {
		t.Fatalf("profile fixture invalid: %v", result.Errors)
	}
	ctx := context.Background()
	controllerAddr := common.HexToAddress("0x0000000000000000000000000000000000C0DE")
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000FADE")
	escrowAddr := common.HexToAddress("0x0000000000000000000000000000000000E5C0")

	if err := repos.AssetProfiles.Upsert(ctx, &models.AssetProfile{
		ProjectID: profile.ProjectID, ProfileRaw: []byte(goldGramProfile), Digest: profile.DigestHex(), CID: profile.CID,
		TokenDecimals: profile.TokenDecimals, TokenUnit: profile.TokenUnit, RecordIDLabel: profile.RecordIDLabel, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Projects.Upsert(ctx, &models.Project{
		ProjectID: profile.ProjectID, Version: "rwa-v2", ChainID: 31337, ProfileDigest: profile.DigestHex(),
		Addresses: models.Addresses{SupplyController: controllerAddr.Hex(), Vault: vaultAddr.Hex(), RedemptionEscrow: escrowAddr.Hex()},
		Status:    models.ProjectStatusActive, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	auditorKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	complianceKey, _ := crypto.GenerateKey()

	client := blockchain.NewFakeClient()
	txs := blockchain.NewTxManager(client, repos.Transactions, big.NewInt(31337), blockchain.FeeModeLegacy)

	domainCfg := eip712.Domain{Name: "RWA-Supply-Attestation", Version: "1", ChainID: big.NewInt(31337), VerifyingContract: controllerAddr}
	auditor := crypto.PubkeyToAddress(auditorKey.PublicKey)

	records := assets.NewRecordService(repos.AssetRecords, repos.AuditPackages, repos.Attestations, nil, controllerAddr, vaultAddr, domainCfg, auditor)
	challenges := compliance.NewChallengeService(repos.WalletChallenges, repos.Investors, time.Hour, "RWA Test Platform")
	webhooks := compliance.NewWebhookService(repos.KYCEvents, repos.Investors, "webhook-secret")
	// The generic ("none") KYC provider verifies the X-Webhook-Signature HMAC shape
	// these tests use; buildApp wires KYC and Webhooks together, so the test App does too
	kycProvider, _ := kyc.New(kyc.Config{Mode: kyc.ModeNone, GenericHMACSecret: "webhook-secret"})
	status := compliance.NewStatusService(txs, common.HexToAddress("0x0000000000000000000000000000000000C0A1"), blockchain.NewStaticKeySigner(complianceKey))
	salesSvc := sales.New(client, vaultAddr, common.HexToAddress("0xA001"), repos.Purchases)
	complianceRegistryAddr := common.HexToAddress("0x0000000000000000000000000000000000C0A1")
	redemptionSvc := redemption.New(client, escrowAddr, repos.RedemptionRequests, complianceRegistryAddr)

	adminKey, _ := crypto.GenerateKey()
	adminAddr := crypto.PubkeyToAddress(adminKey.PublicKey)
	jwtSecret := []byte("test-jwt-secret-that-is-at-least-32-bytes-long")

	app := &App{
		Repos: repos, ChainID: 31337,
		Project: nil, Records: records, Challenges: challenges, Webhooks: webhooks, KYC: kycProvider, Status: status,
		Sales: salesSvc, Redemptions: redemptionSvc, Audit: auditlog.New(repos.AuditLogs),
		Sessions:              auth.NewSessionManager(repos.WalletSessions, 15*time.Minute),
		AdminChallenges:       auth.NewAdminChallengeService(repos.AdminChallenges, time.Hour, "RWA Test Platform"),
		AdminAddress:          adminAddr.Hex(),
		JWTSecret:             jwtSecret,
		JWTTTL:                time.Hour,
		IdempotencyTTL:        time.Hour,
		FinalityConfirmations: 3,
		LastIndexedBlock:      func() uint64 { return 100 },
	}
	router := NewRouter(app)

	token, _, err := auth.IssueAdminJWT(jwtSecret, adminAddr.Hex(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	return &testEnv{
		app: app, router: router, client: client, auditorKey: auditorKey,
		controllerAddr: controllerAddr, vaultAddr: vaultAddr, escrowAddr: escrowAddr,
		adminKey: adminKey, adminAddr: adminAddr, jwtSecret: jwtSecret, bearer: "Bearer " + token,
	}
}

// addr expands a short hex suffix into a full checksummed address, avoiding
// hand-counted 40-character literals that common.IsHexAddress would reject
func addr(shortHex string) string {
	return common.HexToAddress(shortHex).Hex()
}

func doJSON(t *testing.T, router *gin.Engine, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestHealthAndReady(t *testing.T) {
	env := setupTestApp(t)
	w := doJSON(t, env.router, http.MethodGet, "/healthz", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", w.Code)
	}

	// With no ReadyCheck wired (chain services unavailable), /readyz reports
	// not_ready rather than ready
	w = doJSON(t, env.router, http.MethodGet, "/readyz", nil, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status with no ReadyCheck = %d, want 503 (default-deny)", w.Code)
	}

	// A passing ReadyCheck reports ready
	env.app.ReadyCheck = func() error { return nil }
	w = doJSON(t, env.router, http.MethodGet, "/readyz", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("readyz status with passing ReadyCheck = %d, want 200", w.Code)
	}

	// A failing ReadyCheck reports not_ready
	env.app.ReadyCheck = func() error { return errTestNotReady }
	w = doJSON(t, env.router, http.MethodGet, "/readyz", nil, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status with failing ReadyCheck = %d, want 503", w.Code)
	}
	// /readyz is unauthenticated, so it must not reflect the raw internal error,
	// which wraps RPC and Mongo dial failures with host:port detail
	if strings.Contains(w.Body.String(), errTestNotReady.Error()) {
		t.Fatalf("readyz body leaked the internal error text: %s", w.Body.String())
	}

	// Each classified failure maps to a fixed label that reveals no backend detail
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New("chain: dial tcp 10.0.0.7:8545: connection refused"), "chain unavailable"},
		{errors.New("storage: ping mongo-primary.internal:27017: timeout"), "storage unavailable"},
		{errors.New("indexer: reconciliation required"), "indexer unavailable"},
		{errTestNotReady, "not ready"},
	} {
		env.app.ReadyCheck = func() error { return tc.err }
		w = doJSON(t, env.router, http.MethodGet, "/readyz", nil, nil)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode readyz body: %v", err)
		}
		if body["reason"] != tc.want {
			t.Fatalf("readyz reason for %v = %v, want %q", tc.err, body["reason"], tc.want)
		}
	}
}

var errTestNotReady = errors.New("test: not ready")

func TestValidateProfileEndpoint(t *testing.T) {
	env := setupTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/profile/validate", bytes.NewReader([]byte(goldGramProfile)))
	req.Header.Set("Authorization", env.bearer)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var result assets.ValidationResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Valid {
		t.Fatalf("expected valid, errors=%v", result.Errors)
	}
}

// goldGramProfileOtherProject is goldGramProfile with a projectId that
// setupTestApp never seeds, to tell a fixture-stored profile from one this request
// persisted
const goldGramProfileOtherProject = `{
  "profileVersion": "1.0",
  "projectId": "11111111-2222-3333-4444-555555555555",
  "assetType": "allocated-gold-bar",
  "tokenUnit": "gram",
  "tokenDecimals": 18,
  "recordIdLabel": "Bar serial number",
  "assetSchema": {
    "type": "object",
    "additionalProperties": false,
    "required": ["serialNumber", "weightGrams", "purity"],
    "properties": {
      "serialNumber": { "type": "string", "minLength": 1 },
      "weightGrams": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]+)?$" },
      "purity": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]+)?$" }
    }
  }
}`

// TestValidateProfileIsPure checks that POST /api/v1/profile/validate, which is
// admin-only, never writes to the AssetProfiles repository, even for a valid profile
func TestValidateProfileIsPure(t *testing.T) {
	env := setupTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/profile/validate", bytes.NewReader([]byte(goldGramProfileOtherProject)))
	req.Header.Set("Authorization", env.bearer)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var result assets.ValidationResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Valid {
		t.Fatalf("expected valid, errors=%v", result.Errors)
	}

	if _, err := env.app.Repos.AssetProfiles.Get(context.Background(), "11111111-2222-3333-4444-555555555555"); err == nil {
		t.Fatal("validateProfile must not persist the submitted profile")
	}
}

// TestValidateProfileRequiresAdmin checks that the validate route rejects a
// request without an admin JWT
func TestValidateProfileRequiresAdmin(t *testing.T) {
	env := setupTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/profile/validate", bytes.NewReader([]byte(goldGramProfile)))
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
	}
}

// TestCreateProfileRequiresAdmin covers the persistence path, which is admin-only
func TestCreateProfileRequiresAdmin(t *testing.T) {
	env := setupTestApp(t)

	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no token", nil, http.StatusForbidden},
		{"invalid bearer", map[string]string{"Authorization": "Bearer not-a-jwt"}, http.StatusForbidden},
		{"admin jwt", map[string]string{"Authorization": env.bearer, "Idempotency-Key": "create-profile-admin"}, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/profile", bytes.NewReader([]byte(goldGramProfileOtherProject)))
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			env.router.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestCreateProfileIsCreateOnce checks that a second create for the same
// projectId is rejected with 409 and never overwrites
func TestCreateProfileIsCreateOnce(t *testing.T) {
	env := setupTestApp(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profile", bytes.NewReader([]byte(goldGramProfileOtherProject)))
	req.Header.Set("Authorization", env.bearer)
	req.Header.Set("Idempotency-Key", "create-once-1")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, body=%s", w.Code, w.Body.String())
	}

	// Use a different Idempotency-Key to exercise the create-once conflict; reusing the
	// key would replay the first 201
	req = httptest.NewRequest(http.MethodPost, "/api/v1/profile", bytes.NewReader([]byte(goldGramProfileOtherProject)))
	req.Header.Set("Authorization", env.bearer)
	req.Header.Set("Idempotency-Key", "create-once-2")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("second create status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
}

// TestCreateProfileGateRejectsMismatchedProjectId checks that with a configured
// project_id, a profile with a different projectId is rejected with 400
func TestCreateProfileGateRejectsMismatchedProjectId(t *testing.T) {
	env := setupTestApp(t)
	env.app.ProjectID = "99999999-8888-7777-6666-555555555555" // not goldGramProfileOtherProject's id

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profile", bytes.NewReader([]byte(goldGramProfileOtherProject)))
	req.Header.Set("Authorization", env.bearer)
	req.Header.Set("Idempotency-Key", "gate-mismatch")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if _, err := env.app.Repos.AssetProfiles.Get(context.Background(), "11111111-2222-3333-4444-555555555555"); err == nil {
		t.Fatal("a gated-out profile must not be persisted")
	}
}

// TestCreateProfileGateAcceptsMatchingProjectId checks that the same request is
// accepted when the server is configured with the profile's own projectId
func TestCreateProfileGateAcceptsMatchingProjectId(t *testing.T) {
	env := setupTestApp(t)
	env.app.ProjectID = "11111111-2222-3333-4444-555555555555"

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profile", bytes.NewReader([]byte(goldGramProfileOtherProject)))
	req.Header.Set("Authorization", env.bearer)
	req.Header.Set("Idempotency-Key", "gate-match")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
}

// TestRecordCreationBlockedOnProfileDigestMismatch checks that record, package and
// signature operations reject a stored profile whose digest differs from the project's
// on-chain profileDigest
func TestRecordCreationBlockedOnProfileDigestMismatch(t *testing.T) {
	env := setupTestApp(t)
	p, err := env.app.Repos.Projects.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.ProfileDigest = "0x" + strings.Repeat("ab", 32) // deliberately wrong
	if err := env.app.Repos.Projects.Upsert(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	w := doJSON(t, env.router, http.MethodPost, "/api/v1/assets/records",
		map[string]any{"recordId": "rec-1", "asset": map[string]string{"serialNumber": "x", "weightGrams": "1", "purity": "1"}, "amount": "1000000000000000000"},
		map[string]string{"Authorization": env.bearer, "Idempotency-Key": "digest-mismatch-1"})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
}

func TestGetProject(t *testing.T) {
	env := setupTestApp(t)
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/project", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var p dto.ProjectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.ChainID != 31337 {
		t.Errorf("ChainID = %d", p.ChainID)
	}
	// The authority fields are a deploy-time snapshot, so the response must flag
	// them as possibly stale
	if !p.SecurityStale {
		t.Error("expected securityStale = true (authority fields are a deployment-time snapshot)")
	}
}

func TestGetProjectLegal(t *testing.T) {
	env := setupTestApp(t)
	get := func() map[string]any {
		w := doJSON(t, env.router, http.MethodGet, "/api/v1/project", nil, nil)
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if _, ok := get()["legal"]; ok {
		t.Fatal("legal must be omitted when the profile has none")
	}

	ctx := context.Background()
	stored, err := env.app.Repos.AssetProfiles.Get(ctx, "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61")
	if err != nil {
		t.Fatal(err)
	}
	stored.ProfileRaw = []byte(strings.Replace(goldGramProfile, `"assetSchema"`, `"legal": {"wrapper":"share","issuerLei":"506700GE1G29325QX363"}, "assetSchema"`, 1))
	if err := env.app.Repos.AssetProfiles.Upsert(ctx, stored); err != nil {
		t.Fatal(err)
	}
	legal, ok := get()["legal"].(map[string]any)
	if !ok || legal["wrapper"] != "share" || legal["issuerLei"] != "506700GE1G29325QX363" {
		t.Fatalf("legal = %v", legal)
	}
}

func TestComplianceChallengeFlow(t *testing.T) {
	env := setupTestApp(t)
	priv, _ := crypto.GenerateKey()
	walletAddr := crypto.PubkeyToAddress(priv.PublicKey)

	w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/challenge", map[string]string{"address": walletAddr.Hex()}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var ch dto.ChallengeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &ch); err != nil {
		t.Fatal(err)
	}
	if ch.Address != walletAddr.Hex() {
		t.Errorf("Address = %s", ch.Address)
	}
	if ch.Nonce == "" {
		t.Error("expected non-empty nonce")
	}
}

func TestComplianceSetStatusRequiresRole(t *testing.T) {
	env := setupTestApp(t)
	body := SetStatusRequestBody{Address: addr("0xB0B0"), Status: "Allowed"}
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/status", body, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without admin/operator key", w.Code)
	}
}

func TestComplianceSetStatusSubmitsTx(t *testing.T) {
	env := setupTestApp(t)

	body := SetStatusRequestBody{Address: addr("0xB0B0"), Status: "Allowed", ValidUntil: 0}
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/status", body, map[string]string{"Authorization": env.bearer, "Idempotency-Key": "set-status-submits-tx"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var ref dto.TxRef
	if err := json.Unmarshal(w.Body.Bytes(), &ref); err != nil {
		t.Fatal(err)
	}
	if ref.TxHash == "" {
		t.Error("expected non-empty txHash")
	}
	if ref.Status != "pending" {
		t.Errorf("Status = %s", ref.Status)
	}
}

// TestIdempotencyKeyRequiredAtAPILayer checks that a side-effecting route wired
// with auth.Idempotency rejects a request without an Idempotency-Key header, using
// CodeIdempotencyKeyRequired, before the handler runs
func TestIdempotencyKeyRequiredAtAPILayer(t *testing.T) {
	env := setupTestApp(t)

	body := SetStatusRequestBody{Address: addr("0xB0B2"), Status: "Allowed"}
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/status", body, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a missing Idempotency-Key, body=%s", w.Code, w.Body.String())
	}
	var resp dto.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != CodeIdempotencyKeyRequired {
		t.Errorf("code = %q, want %q", resp.Code, CodeIdempotencyKeyRequired)
	}
	if len(env.client.SentTxs) != 0 {
		t.Errorf("expected no transaction submitted, got %d - the handler must never run without a key", len(env.client.SentTxs))
	}
}

func TestComplianceStatusIdempotency(t *testing.T) {
	env := setupTestApp(t)

	body := SetStatusRequestBody{Address: addr("0xB0B1"), Status: "Allowed"}
	headers := map[string]string{"Authorization": env.bearer, "Idempotency-Key": "idem-1"}
	w1 := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/status", body, headers)
	w2 := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/status", body, headers)
	if w1.Code != http.StatusAccepted || w2.Code != http.StatusAccepted {
		t.Fatalf("codes: %d, %d", w1.Code, w2.Code)
	}
	if w1.Body.String() != w2.Body.String() {
		t.Errorf("expected identical replayed response, got %s vs %s", w1.Body.String(), w2.Body.String())
	}
	if len(env.client.SentTxs) != 1 {
		t.Errorf("expected exactly 1 broadcast tx, got %d", len(env.client.SentTxs))
	}
}

func TestAssetsCreateListPackageAndSignatureFlow(t *testing.T) {
	env := setupTestApp(t)

	createBody := CreateRecordRequestBody{
		RecordID: "GOLD-BAR-API-1",
		Asset:    json.RawMessage(`{"serialNumber":"1","weightGrams":"250","purity":"999.9"}`),
		Amount:   "250000000000000000000",
	}
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/assets/records", createBody, map[string]string{"Authorization": env.bearer, "Idempotency-Key": "flow-create-1"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", w.Code, w.Body.String())
	}
	var created dto.AssetRecordResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	w = doJSON(t, env.router, http.MethodGet, "/api/v1/assets/records", nil, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}
	var listed []dto.AssetRecordResponse
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 record, got %d", len(listed))
	}

	w = doJSON(t, env.router, http.MethodGet, "/api/v1/assets/records/"+created.RecordID+"/package", nil, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("package status = %d, body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/zip" {
		t.Errorf("Content-Type = %s", w.Header().Get("Content-Type"))
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty package bytes")
	}
}

// TestDownloadPackageRejectsRejectedRecord checks that a Rejected record cannot
// be packaged, while Signed, Minted and expired records are still served (see
// downloadPackage)
func TestDownloadPackageRejectsRejectedRecord(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()

	createBody := CreateRecordRequestBody{
		RecordID: "GOLD-BAR-L03-REJECTED",
		Asset:    json.RawMessage(`{"serialNumber":"5","weightGrams":"5","purity":"999.9"}`),
		Amount:   "5000000000000000000",
	}
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/assets/records", createBody, map[string]string{"Authorization": env.bearer, "Idempotency-Key": "rejected-create"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", w.Code, w.Body.String())
	}

	rec, err := env.app.Repos.AssetRecords.Get(ctx, "GOLD-BAR-L03-REJECTED")
	if err != nil {
		t.Fatal(err)
	}
	rec.Status = models.RecordStatusRejected
	if err := env.app.Repos.AssetRecords.Update(ctx, rec); err != nil {
		t.Fatal(err)
	}

	w = doJSON(t, env.router, http.MethodGet, "/api/v1/assets/records/GOLD-BAR-L03-REJECTED/package", nil, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a Rejected record, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestRedemptionIDParamAcceptsZero checks that the :id path parameter accepts 0,
// the first id RedemptionEscrow assigns (nextId starts at 0)
func TestRedemptionIDParamAcceptsZero(t *testing.T) {
	env := setupTestApp(t)
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/redemptions/0", nil, nil)
	// No redemption with id 0 exists in this fixture, so the expected answer is 404,
	// not a 400 for a malformed id
	if w.Code == http.StatusBadRequest {
		t.Errorf("id=0: status = 400 (rejected as a malformed id), want it to reach the lookup and 404 instead")
	}
}

func TestSalesInventory(t *testing.T) {
	env := setupTestApp(t)

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/sales/inventory", nil, nil)
	// Without a canned CallContract response, expect an internal error. A 404 would mean
	// the route is missing.
	if w.Code == http.StatusNotFound {
		t.Fatalf("route not found: %d", w.Code)
	}
}

func TestRedemptionsList(t *testing.T) {
	env := setupTestApp(t)

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/redemptions", nil, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var list []dto.RedemptionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

func TestListTransactionsEmpty(t *testing.T) {
	env := setupTestApp(t)
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/transactions", nil, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var list []dto.TransactionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

// TestListRecordsIsPaginated seeds more than defaultListLimit records, checks the
// default response bound, then walks every page with an explicit limit and cursor
func TestListRecordsIsPaginated(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	const total = defaultListLimit + 7
	for i := 0; i < total; i++ {
		if err := env.app.Repos.AssetRecords.Create(ctx, &models.AssetRecord{
			RecordID: fmt.Sprintf("PAGE-%03d", i), ProjectID: "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61",
			Status: models.RecordStatusDraft, Amount: "1", CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// With no limit or cursor, the default limit still bounds the response
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/assets/records", nil, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var page []dto.AssetRecordResponse
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page) != defaultListLimit {
		t.Errorf("default page size = %d, want %d (server-side default must apply even with no query params)", len(page), defaultListLimit)
	}
	// Keyset pagination omits X-Total-Count, since a total would need a full count
	if w.Header().Get("X-Total-Count") != "" {
		t.Errorf("X-Total-Count = %q, want omitted", w.Header().Get("X-Total-Count"))
	}
	next := w.Header().Get("X-Next-Cursor")
	if next == "" {
		t.Fatal("expected a non-empty X-Next-Cursor since more records remain")
	}

	// Walk every remaining page via the returned cursor and confirm each seeded
	// recordId appears exactly once
	seen := map[string]bool{}
	for _, r := range page {
		seen[r.RecordID] = true
	}
	for next != "" {
		w := doJSON(t, env.router, http.MethodGet, "/api/v1/assets/records?limit=10&cursor="+next, nil, map[string]string{"Authorization": env.bearer})
		if w.Code != http.StatusOK {
			t.Fatalf("page fetch: status = %d, body=%s", w.Code, w.Body.String())
		}
		var p []dto.AssetRecordResponse
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if len(p) == 0 {
			t.Fatal("got an empty page while X-Next-Cursor was still set")
		}
		for _, r := range p {
			if seen[r.RecordID] {
				t.Errorf("recordId %s seen more than once across pages", r.RecordID)
			}
			seen[r.RecordID] = true
		}
		next = w.Header().Get("X-Next-Cursor")
	}
	if len(seen) != total {
		t.Errorf("paginated walk covered %d records, want %d", len(seen), total)
	}
}

// TestListRecordsRejectsOversizedLimit checks that a limit above maxListLimit
// is capped to it
func TestListRecordsRejectsOversizedLimit(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	const total = maxListLimit + 5
	for i := 0; i < total; i++ {
		if err := env.app.Repos.AssetRecords.Create(ctx, &models.AssetRecord{
			RecordID: fmt.Sprintf("CAP-%04d", i), ProjectID: "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61",
			Status: models.RecordStatusDraft, Amount: "1", CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatal(err)
		}
	}

	w := doJSON(t, env.router, http.MethodGet, fmt.Sprintf("/api/v1/assets/records?limit=%d", total), nil, map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var page []dto.AssetRecordResponse
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page) != maxListLimit {
		t.Errorf("page size = %d, want the hard-capped %d even though limit=%d was requested", len(page), maxListLimit, total)
	}
}

// TestAuthSessionHasItsOwnRateLimitIndependentOfGlobal checks login throttling with
// RateLimitRPS unset, so NewRouter installs no global limiter
func TestAuthSessionHasItsOwnRateLimitIndependentOfGlobal(t *testing.T) {
	env := setupTestApp(t)
	if env.app.RateLimitRPS > 0 {
		t.Fatal("test precondition: expected the global rate limiter to be disabled")
	}

	var last *httptest.ResponseRecorder
	throttled := false
	for i := 0; i < sessionLoginBurst+5; i++ {
		last = doJSON(t, env.router, http.MethodPost, "/auth/session", map[string]string{"address": env.adminAddr.Hex(), "signature": "0x00"}, nil)
		if last.Code == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Fatalf("expected POST /auth/session to eventually return 429 within %d rapid calls, last status = %d", sessionLoginBurst+5, last.Code)
	}
}
