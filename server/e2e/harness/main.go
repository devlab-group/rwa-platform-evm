// Command harness drives the full self-hosted RWA platform lifecycle
// end-to-end over the platform server's documented HTTP API (api/openapi.yaml)
// against a live server + anvil + Mongo stack that server/e2e/run_e2e.sh has
// booted with ONLY the RWAFactory deployed (bootstrap config). The harness
// itself performs every wallet action the way the admin's web wallet would:
// it broadcasts RWAFactory.deploy(config) and waits for the server to OBSERVE
// the ProjectDeployed event and mark the project Active (deployment is
// permissionless, not a server action); then wallet-ownership challenge ->
// compliance allow -> asset record -> .rwa package download -> offline
// signer-CLI signature -> broadcast SupplyController.mint(attestation,
// signature) and wait for the server to observe the Minted event -> buy ->
// request redemption -> fund -> claim, plus a second request that times out
// and is cancelled.
//
// It is a plain Go program (not a bash+curl script) because this sandbox's
// permission policy denies direct curl/wget invocations; net/http from a Go
// binary is unaffected, and go-ethereum is already a server module
// dependency, so every wallet transaction (deploy, mint, buy, redemption
// funding — none of which are server hot-key actions)
// is signed and submitted directly here rather than shelling out to
// `cast send`.
//
// All configuration is read from the environment; see run_e2e.sh. Exits 0
// and prints "API E2E OK" only if every step (including every read-model
// assertion) succeeds.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/rwa-platform/server/internal/auditpkg"
	"github.com/rwa-platform/server/internal/bindings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "harness: FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("API E2E OK")
}

// ---- configuration ---------------------------------------------------

type config struct {
	baseURL string
	rpcURL  string
	chainID *big.Int
	// adminBearer is the admin JWT acquired at runtime via the wallet-signature
	// login (POST /auth/challenge + /auth/session), signed with deployerPK —
	// which is the configured admin address. Attached as Authorization: Bearer
	// on every admin-only request.
	adminBearer string
	deployerPK  string // anvil acct0: admin/auditor/treasurer/compliance-operator/etc (the compliance hot key reuses this too)
	investorPK  string // anvil acct1
	// quoteAddr is the pre-deployed quote/collateral MockERC20 whose address
	// goes into the ProjectConfig the harness broadcasts to RWAFactory.deploy.
	quoteAddr common.Address
	// projectUUID is the off-chain project identity. The on-chain
	// ProjectConfig.projectId MUST be keccak256(bytes(projectUUID)) — the same
	// derivation the server's project.ReconcileDeployment expects — or the
	// deployment projector will not bind the ProjectDeployed event to the
	// stored profile.
	projectUUID     string
	purchasePrice   *big.Int
	redemptionPrice *big.Int
	// The remaining deployed-contract addresses are NOT known up front: the
	// harness broadcasts RWAFactory.deploy itself and reads them back from GET
	// /api/v1/project once the server has observed the event and marked the
	// project Active. auditorAddr == the deployer (single-operator V1).
	supplyCtrlAddr common.Address
	vaultAddr      common.Address
	tokenAddr      common.Address
	escrowAddr     common.Address
	auditorAddr    common.Address
	signerBin      string
	keystorePath   string
	keystorePass   string
	workDir        string
	redeemTimeout  int64
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	cfg := config{
		baseURL:      envOr("E2E_BASE_URL", "http://127.0.0.1:8090"),
		rpcURL:       envOr("CHAIN_RPC_URL", "http://127.0.0.1:8545"),
		deployerPK:   os.Getenv("DEPLOYER_PK"),
		investorPK:   os.Getenv("INVESTOR_PK"),
		signerBin:    os.Getenv("SIGNER_BIN"),
		keystorePath: os.Getenv("KEYSTORE_PATH"),
		keystorePass: os.Getenv("KEYSTORE_PASSWORD_FILE"),
		workDir:      envOr("E2E_WORKDIR", "."),
		projectUUID:  envOr("E2E_PROJECT_ID", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"),
	}
	for _, req := range []struct{ name, val string }{
		{"DEPLOYER_PK", cfg.deployerPK}, {"INVESTOR_PK", cfg.investorPK},
		{"SIGNER_BIN", cfg.signerBin}, {"KEYSTORE_PATH", cfg.keystorePath}, {"KEYSTORE_PASSWORD_FILE", cfg.keystorePass},
		{"QUOTE_TOKEN_ADDRESS", os.Getenv("QUOTE_TOKEN_ADDRESS")},
	} {
		if req.val == "" {
			return config{}, fmt.Errorf("required env var %s is unset/empty", req.name)
		}
	}
	cfg.quoteAddr = common.HexToAddress(os.Getenv("QUOTE_TOKEN_ADDRESS"))

	chainID, ok := new(big.Int).SetString(envOr("CHAIN_ID", "31337"), 10)
	if !ok {
		return config{}, fmt.Errorf("invalid CHAIN_ID")
	}
	cfg.chainID = chainID

	timeout, ok := new(big.Int).SetString(envOr("REDEMPTION_TIMEOUT", "86400"), 10)
	if !ok {
		return config{}, fmt.Errorf("invalid REDEMPTION_TIMEOUT")
	}
	cfg.redeemTimeout = timeout.Int64()

	cfg.purchasePrice, ok = new(big.Int).SetString(envOr("E2E_PURCHASE_PRICE", "2000000"), 10)
	if !ok {
		return config{}, fmt.Errorf("invalid E2E_PURCHASE_PRICE")
	}
	cfg.redemptionPrice, ok = new(big.Int).SetString(envOr("E2E_REDEMPTION_PRICE", "1950000"), 10)
	if !ok {
		return config{}, fmt.Errorf("invalid E2E_REDEMPTION_PRICE")
	}

	return cfg, nil
}

var (
	amount1000 = mustBig("1000000000000000000000") // 1000 RWA (18 dec) minted to the Vault
	amount100  = mustBig("100000000000000000000")  // 100 RWA bought by the investor
	amount40   = mustBig("40000000000000000000")   // 40 RWA redeemed in the primary request
	amount10   = mustBig("10000000000000000000")   // 10 RWA redeemed in the timeout->cancel demo
	amount25   = mustBig("25000000000000000000")   // 25 RWA frozen against the investor
	amount20   = mustBig("20000000000000000000")   // the 20 RWA still frozen after the seizure
)

func mustBig(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad literal " + s)
	}
	return n
}

// ---- run ----------------------------------------------------------------

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx := context.Background()

	eth, err := ethclient.DialContext(ctx, cfg.rpcURL)
	if err != nil {
		return fmt.Errorf("dial anvil: %w", err)
	}
	defer eth.Close()

	deployerKey, err := crypto.HexToECDSA(strings.TrimPrefix(cfg.deployerPK, "0x"))
	if err != nil {
		return fmt.Errorf("parse DEPLOYER_PK: %w", err)
	}
	investorKey, err := crypto.HexToECDSA(strings.TrimPrefix(cfg.investorPK, "0x"))
	if err != nil {
		return fmt.Errorf("parse INVESTOR_PK: %w", err)
	}
	investorAddr := crypto.PubkeyToAddress(investorKey.PublicKey)
	deployerAddr := crypto.PubkeyToAddress(deployerKey.PublicKey)
	// Single-operator V1: the deployer wallet is admin/auditor/treasurer/etc.
	cfg.auditorAddr = deployerAddr

	api := &apiClient{base: cfg.baseURL, hc: &http.Client{Timeout: 20 * time.Second}}
	chain := &chainClient{eth: eth, chainID: cfg.chainID}

	step("wait for platform server readiness")
	if err := waitServerReady(api); err != nil {
		return fmt.Errorf("server readiness: %w", err)
	}

	step("admin wallet login: challenge + JWT for " + deployerAddr.Hex())
	bearer, err := authenticateAdmin(api, deployerKey, deployerAddr)
	if err != nil {
		return fmt.Errorf("admin login: %w", err)
	}
	cfg.adminBearer = bearer

	step("GET /healthz, /readyz: confirm the platform is up on the right chain")
	if err := checkHealthReady(api, cfg); err != nil {
		return fmt.Errorf("health/ready check: %w", err)
	}

	step("asset profile: validate then create (admin)")
	profileDigest, err := validateProfile(api, cfg)
	if err != nil {
		return fmt.Errorf("profile validate/create: %w", err)
	}

	step("deploy project: broadcast RWAFactory.deploy(config) from the admin wallet")
	if err := deployProject(ctx, api, chain, &cfg, deployerKey, deployerAddr, profileDigest); err != nil {
		return fmt.Errorf("deploy project: %w", err)
	}

	step("wallet-ownership challenge for investor " + investorAddr.Hex())
	if err := walletOwnership(api, investorKey, investorAddr); err != nil {
		return fmt.Errorf("wallet ownership: %w", err)
	}

	step("compliance: allow investor")
	if err := allowInvestor(api, cfg, investorAddr); err != nil {
		return fmt.Errorf("compliance allow: %w", err)
	}

	step("asset record: create")
	recordID, err := createRecord(api, cfg)
	if err != nil {
		return fmt.Errorf("create record: %w", err)
	}

	step("asset record: download .rwa package")
	pkgPath, err := downloadPackage(api, cfg, recordID)
	if err != nil {
		return fmt.Errorf("download package: %w", err)
	}

	step("offline signer CLI: sign package")
	signedResultPath, err := signPackage(cfg, pkgPath, profileDigest)
	if err != nil {
		return fmt.Errorf("signer CLI: %w", err)
	}

	step("broadcast SupplyController.mint(attestation, signature) from the admin wallet")
	if err := mintOnChain(ctx, chain, cfg, deployerKey, pkgPath, signedResultPath); err != nil {
		return fmt.Errorf("mint on-chain: %w", err)
	}

	step("wait for record status Minted (server observes the Minted event)")
	if err := waitRecordMinted(api, cfg, recordID); err != nil {
		return fmt.Errorf("wait minted: %w", err)
	}

	step("buy")
	if err := buy(ctx, api, chain, cfg, investorKey, investorAddr); err != nil {
		return fmt.Errorf("buy: %w", err)
	}

	step("request redemption (primary, 40 RWA)")
	reqID1, err := requestRedemption(ctx, api, chain, cfg, investorKey, investorAddr, amount40)
	if err != nil {
		return fmt.Errorf("request redemption: %w", err)
	}

	step("fund + claim redemption " + reqID1)
	if err := fundAndClaim(ctx, api, chain, cfg, deployerKey, deployerAddr, reqID1); err != nil {
		return fmt.Errorf("fund/claim: %w", err)
	}

	step("timeout -> cancel branch: request redemption (secondary, 10 RWA)")
	reqID2, err := requestRedemption(ctx, api, chain, cfg, investorKey, investorAddr, amount10)
	if err != nil {
		return fmt.Errorf("request redemption (cancel demo): %w", err)
	}

	step("advance chain time past redemptionTimeout and cancel " + reqID2)
	if err := timeoutAndCancel(ctx, api, chain, cfg, investorKey, investorAddr, reqID2); err != nil {
		return fmt.Errorf("timeout/cancel: %w", err)
	}

	step("ERC-7943 enforcement: freeze, seize, release (admin wallet), projected into GET /project/enforcement")
	if err := enforcementLifecycle(ctx, api, chain, cfg, deployerKey, deployerAddr, investorAddr); err != nil {
		return fmt.Errorf("erc-7943 enforcement: %w", err)
	}

	return nil
}

func step(msg string) { fmt.Println("--- " + msg) }

// ---- HTTP API client -------------------------------------------------

type apiClient struct {
	base string
	hc   *http.Client
}

type apiError struct {
	status int
	body   []byte
}

func (e *apiError) Error() string { return fmt.Sprintf("http %d: %s", e.status, string(e.body)) }

func (c *apiClient) do(method, path, bearer string, body []byte, idempotencyKey string) ([]byte, error) {
	req, err := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, &apiError{status: resp.StatusCode, body: buf.Bytes()}
	}
	return buf.Bytes(), nil
}

func (c *apiClient) getJSON(path, bearer string, out any) error {
	raw, err := c.do(http.MethodGet, path, bearer, nil, "")
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *apiClient) postJSON(path, bearer string, in, out any, idempotencyKey string) error {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	raw, err := c.do(http.MethodPost, path, bearer, body, idempotencyKey)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// poll retries fn (done, err) every interval until it returns done=true, an
// error, or timeout elapses. Every read-model assertion in this harness
// (compliance allow, record Minted, redemption status transitions) goes
// through here because the server's projector/reconcile loops only run on
// their own tickers (5-15s; see cmd/platform/main.go's startBackgroundLoops),
// never synchronously with the HTTP call that submitted the transaction.
func poll(desc string, timeout, interval time.Duration, fn func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		done, err := fn()
		if err != nil {
			lastErr = err
		} else if done {
			return nil
		}
		time.Sleep(interval)
	}
	if lastErr != nil {
		return fmt.Errorf("timed out waiting for %s: %w", desc, lastErr)
	}
	return fmt.Errorf("timed out waiting for %s", desc)
}

// ---- chain client: signs/submits investor & treasurer wallet txs ------

type chainClient struct {
	eth     *ethclient.Client
	chainID *big.Int
}

// sendTx signs a DynamicFeeTx from key's address to (to, data), submits it,
// and waits for its mined receipt (anvil instamines on submit, so this
// resolves immediately). Returns an error if the tx reverted.
func (cc *chainClient) sendTx(ctx context.Context, key *ecdsa.PrivateKey, to common.Address, data []byte) (*types.Receipt, error) {
	from := crypto.PubkeyToAddress(key.PublicKey)
	nonce, err := cc.eth.PendingNonceAt(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	head, err := cc.eth.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("head: %w", err)
	}
	tip, err := cc.eth.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, fmt.Errorf("tip cap: %w", err)
	}
	feeCap := new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))
	gasLimit, err := cc.eth.EstimateGas(ctx, ethereum.CallMsg{From: from, To: &to, Data: data})
	if err != nil {
		return nil, fmt.Errorf("estimate gas: %w", err)
	}
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: cc.chainID, Nonce: nonce, GasTipCap: tip, GasFeeCap: feeCap,
		Gas: gasLimit + gasLimit/5, To: &to, Value: big.NewInt(0), Data: data,
	})
	signer := types.LatestSignerForChainID(cc.chainID)
	signedTx, err := types.SignTx(tx, signer, key)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	if err := cc.eth.SendTransaction(ctx, signedTx); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	receipt, err := waitReceipt(ctx, cc.eth, signedTx.Hash())
	if err != nil {
		return nil, err
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return nil, fmt.Errorf("tx %s reverted", signedTx.Hash().Hex())
	}
	return receipt, nil
}

func waitReceipt(ctx context.Context, eth *ethclient.Client, hash common.Hash) (*types.Receipt, error) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		receipt, err := eth.TransactionReceipt(ctx, hash)
		if err == nil {
			return receipt, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("receipt for %s not found before deadline", hash.Hex())
}

// increaseTime and mineEmptyBlock call anvil's evm_* debug RPCs directly
// through ethclient's underlying *rpc.Client (ethclient itself has no
// wrapper for these anvil/hardhat-specific methods). Used for the
// timeout->cancel branch, where redemptionTimeout must actually elapse.
func (cc *chainClient) increaseTime(ctx context.Context, seconds int64) error {
	return cc.eth.Client().CallContext(ctx, nil, "evm_increaseTime", seconds)
}

func (cc *chainClient) mineEmptyBlock(ctx context.Context) error {
	return cc.eth.Client().CallContext(ctx, nil, "evm_mine")
}

// ---- quotes -------------------------------------------------------------
//
// Quotes are read straight off the chain: the server exposes no quote
// endpoint (previewBuy/previewRedeem are plain view functions any client
// reads itself), the same way this harness builds its own calldata rather
// than asking the server for it.

// call performs a plain eth_call against to with data and returns the raw
// return bytes, for the view functions the harness reads itself.
func (cc *chainClient) call(ctx context.Context, to common.Address, data []byte) ([]byte, error) {
	return cc.eth.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
}

// previewBuy reads Vault.previewBuy(tokenAmount): the quote-token amount the
// investor must approve and pass as maxQuoteAmount to Vault.buy.
func (cc *chainClient) previewBuy(ctx context.Context, vaultAddr common.Address, tokenAmount *big.Int) (*big.Int, error) {
	vault := bindings.NewVault()
	data, err := vault.PackPreviewBuy(tokenAmount)
	if err != nil {
		return nil, err
	}
	out, err := cc.call(ctx, vaultAddr, data)
	if err != nil {
		return nil, fmt.Errorf("Vault.previewBuy: %w", err)
	}
	return vault.UnpackPreviewBuy(out)
}

// previewRedeem reads RedemptionEscrow.previewRedeem(rwaAmount): the quote
// amount the investor passes as minQuoteOut to requestRedemption.
func (cc *chainClient) previewRedeem(ctx context.Context, escrowAddr common.Address, rwaAmount *big.Int) (*big.Int, error) {
	escrow := bindings.NewRedemptionEscrow()
	data, err := escrow.PackPreviewRedeem(rwaAmount)
	if err != nil {
		return nil, err
	}
	out, err := cc.call(ctx, escrowAddr, data)
	if err != nil {
		return nil, fmt.Errorf("RedemptionEscrow.previewRedeem: %w", err)
	}
	return escrow.UnpackPreviewRedeem(out)
}

// ---- ERC20 mint/approve ABI (MockERC20.mint is permissionless; approve is
// standard ERC20) ---------------------------------------------------------

var erc20MutABI = mustABIJSON(`[
  {"type":"function","name":"mint","stateMutability":"nonpayable","inputs":[{"name":"to","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"approve","stateMutability":"nonpayable","inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"name":"","type":"bool"}]}
]`)

func mustABIJSON(s string) abi.ABI {
	a, err := abi.JSON(strings.NewReader(s))
	if err != nil {
		panic(err)
	}
	return a
}

func packMint(to common.Address, amount *big.Int) []byte {
	data, err := erc20MutABI.Pack("mint", to, amount)
	if err != nil {
		panic(err)
	}
	return data
}

func packApprove(spender common.Address, amount *big.Int) []byte {
	data, err := erc20MutABI.Pack("approve", spender, amount)
	if err != nil {
		panic(err)
	}
	return data
}

// ---- RedemptionEscrow.fundRedemption ABI --------------------------------
// Funding is a role-gated wallet transaction the treasurer submits directly
// (the server no longer generates its calldata), so the harness encodes it
// itself, mirroring the web's connected-wallet flow.

var redemptionEscrowMutABI = mustABIJSON(`[
  {"type":"function","name":"fundRedemption","stateMutability":"nonpayable","inputs":[{"name":"id","type":"uint256"}],"outputs":[]}
]`)

func packFundRedemption(id *big.Int) []byte {
	data, err := redemptionEscrowMutABI.Pack("fundRedemption", id)
	if err != nil {
		panic(err)
	}
	return data
}

// ---- RWAToken ERC-7943 enforcement ABI ----------------------------------
// Freezing and seizure are admin wallet transactions: the server only observes
// the resulting events, and has no endpoint that signs either one. The harness
// therefore encodes them itself, exactly as the admin console does.

var erc7943MutABI = mustABIJSON(`[
  {"type":"function","name":"setFrozenTokens","stateMutability":"nonpayable","inputs":[
    {"name":"account","type":"address"},{"name":"amount","type":"uint256"}],
    "outputs":[{"name":"result","type":"bool"}]},
  {"type":"function","name":"forcedTransfer","stateMutability":"nonpayable","inputs":[
    {"name":"from","type":"address"},{"name":"to","type":"address"},{"name":"amount","type":"uint256"}],
    "outputs":[{"name":"result","type":"bool"}]}
]`)

func packSetFrozenTokens(account common.Address, amount *big.Int) []byte {
	data, err := erc7943MutABI.Pack("setFrozenTokens", account, amount)
	if err != nil {
		panic(err)
	}
	return data
}

func packForcedTransfer(from, to common.Address, amount *big.Int) []byte {
	data, err := erc7943MutABI.Pack("forcedTransfer", from, to, amount)
	if err != nil {
		panic(err)
	}
	return data
}

// ---- flow steps ---------------------------------------------------------

// authenticateAdmin performs the wallet-signature -> JWT admin login: request
// a challenge for addr, personal_sign the returned message with key, exchange
// it for a JWT. key must control the server's configured admin address.
func authenticateAdmin(api *apiClient, key *ecdsa.PrivateKey, addr common.Address) (string, error) {
	var ch struct {
		Message   string `json:"message"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := api.postJSON("/auth/challenge", "", map[string]string{"address": addr.Hex()}, &ch, ""); err != nil {
		return "", fmt.Errorf("challenge: %w", err)
	}
	sig, err := signPersonal(key, ch.Message)
	if err != nil {
		return "", fmt.Errorf("sign challenge: %w", err)
	}
	var sess struct {
		Token   string `json:"token"`
		Role    string `json:"role"`
		Address string `json:"address"`
	}
	if err := api.postJSON("/auth/session", "", map[string]string{
		"address": addr.Hex(), "signature": "0x" + hex.EncodeToString(sig),
	}, &sess, ""); err != nil {
		return "", fmt.Errorf("session: %w", err)
	}
	if sess.Token == "" || sess.Role != "admin" {
		return "", fmt.Errorf("unexpected session response: role=%q token-empty=%v", sess.Role, sess.Token == "")
	}
	return sess.Token, nil
}

func signPersonal(key *ecdsa.PrivateKey, message string) ([]byte, error) {
	hash := accounts.TextHash([]byte(message))
	sig, err := crypto.Sign(hash, key)
	if err != nil {
		return nil, err
	}
	// go-ethereum's crypto.Sign returns v in {0,1}; personal_sign / ecrecover
	// convention (and this server's eip712.RecoverSigner) expects {27,28}.
	sig[64] += 27
	return sig, nil
}

// waitServerReady polls /healthz until the platform binary (started by
// run_e2e.sh just before invoking this program) has finished connecting to
// Mongo and the chain RPC and is accepting connections. This is the harness's
// own readiness wait specifically so run_e2e.sh never needs to shell out to
// curl/wget for it (this sandbox's permission policy denies both; see the
// package doc comment above).
func waitServerReady(api *apiClient) error {
	return poll("platform server /healthz", 30*time.Second, 500*time.Millisecond, func() (bool, error) {
		_, err := api.do(http.MethodGet, "/healthz", "", nil, "")
		return err == nil, nil
	})
}

// checkHealthReady confirms /healthz and /readyz agree the platform is up and
// bound to the configured chain. Unlike before, it does NOT assert a deployed
// project — nothing is deployed yet at this point (the harness deploys it in
// the next step, then reads the addresses back).
func checkHealthReady(api *apiClient, cfg config) error {
	var health struct {
		Status  string `json:"status"`
		ChainID int64  `json:"chainId"`
	}
	if err := api.getJSON("/healthz", "", &health); err != nil {
		return fmt.Errorf("GET /healthz: %w", err)
	}
	if health.Status != "ok" {
		return fmt.Errorf("GET /healthz: status = %q, want \"ok\"", health.Status)
	}
	if health.ChainID != cfg.chainID.Int64() {
		return fmt.Errorf("GET /healthz: chainId = %d, want %d", health.ChainID, cfg.chainID.Int64())
	}
	if _, err := api.do(http.MethodGet, "/readyz", "", nil, ""); err != nil {
		return fmt.Errorf("GET /readyz: %w", err)
	}
	return nil
}

// projectResponse is the subset of GET /api/v1/project the harness reads.
type projectResponse struct {
	ProjectID        string `json:"projectId"`
	Status           string `json:"status"`
	VerificationNote string `json:"verificationNote"`
	ChainID          int64  `json:"chainId"`
	Decimals         uint8  `json:"decimals"`
	Addresses        struct {
		Token            string `json:"token"`
		Compliance       string `json:"compliance"`
		SupplyController string `json:"supplyController"`
		Vault            string `json:"vault"`
		RedemptionEscrow string `json:"redemptionEscrow"`
		Strategy         string `json:"strategy"`
		QuoteToken       string `json:"quoteToken"`
	} `json:"addresses"`
	Auditor string `json:"auditor"`
}

// deployProject broadcasts RWAFactory.deploy(config) from the admin wallet —
// deployment is permissionless and no longer a server action — then polls GET
// /api/v1/project until the server's deployment projector
// (project.ReconcileDeployment) has observed the ProjectDeployed event,
// verified the stack, and marked it Active. It populates cfg with the deployed
// addresses read back from the project record so the rest of the lifecycle
// (records, buy, redemption) runs against the freshly deployed stack.
func deployProject(ctx context.Context, api *apiClient, chain *chainClient, cfg *config, deployerKey *ecdsa.PrivateKey, deployerAddr common.Address, profileDigest string) error {
	// The factory address is a public bootstrap parameter served by GET /config.
	var boot struct {
		ChainID        int64  `json:"chainId"`
		FactoryAddress string `json:"factoryAddress"`
	}
	if err := api.getJSON("/api/v1/config", "", &boot); err != nil {
		return fmt.Errorf("GET /api/v1/config: %w", err)
	}
	if !common.IsHexAddress(boot.FactoryAddress) || common.HexToAddress(boot.FactoryAddress) == (common.Address{}) {
		return fmt.Errorf("GET /api/v1/config returned no factory address (got %q)", boot.FactoryAddress)
	}
	factory := common.HexToAddress(boot.FactoryAddress)

	digestBytes, err := hexToBytes32Local(profileDigest)
	if err != nil {
		return fmt.Errorf("profileDigest %q: %w", profileDigest, err)
	}
	// projectId = keccak256(bytes(UUID)) — the exact derivation the server's
	// ReconcileDeployment uses to bind the event to the stored profile.
	projectID := crypto.Keccak256Hash([]byte(cfg.projectUUID))

	config := bindings.ProjectConfig{
		Name: "Gold Bar Token", Symbol: "GBT", Decimals: 18,
		ProfileDigest: digestBytes, ProjectID: projectID,
		QuoteToken:                   cfg.quoteAddr,
		PurchasePricePerWholeToken:   cfg.purchasePrice,
		RedemptionPricePerWholeToken: cfg.redemptionPrice,
		RedemptionTimeout:            uint64(cfg.redeemTimeout),
		Admin:                        deployerAddr,
		Auditor:                      deployerAddr,
		ComplianceOperator:           deployerAddr,
		Pricer:                       deployerAddr,
		Treasurer:                    deployerAddr,
		RedemptionManager:            deployerAddr,
		Treasury:                     deployerAddr,
		AdminTransferDelay:           big.NewInt(0),
	}
	data, err := bindings.NewFactory().PackDeploy(config)
	if err != nil {
		return fmt.Errorf("encode deploy calldata: %w", err)
	}
	if _, err := chain.sendTx(ctx, deployerKey, factory, data); err != nil {
		return fmt.Errorf("broadcast RWAFactory.deploy: %w", err)
	}

	// The server must observe ProjectDeployed, run VerifyDeployment, and reach
	// Active. Generous window: the indexer poll (5s) + reconcile ticker (10s).
	var proj projectResponse
	if err := poll("project reaches Active (server-observed deploy + verification)", 120*time.Second, 3*time.Second, func() (bool, error) {
		if err := api.getJSON("/api/v1/project", "", &proj); err != nil {
			return false, nil // 404 until the projector creates the record — keep polling
		}
		switch proj.Status {
		case "Active":
			return true, nil
		case "Failed":
			return false, fmt.Errorf("deployment verification FAILED: %s", proj.VerificationNote)
		default:
			return false, nil
		}
	}); err != nil {
		return err
	}

	// Populate cfg with the server-verified deployed addresses.
	cfg.tokenAddr = common.HexToAddress(proj.Addresses.Token)
	cfg.supplyCtrlAddr = common.HexToAddress(proj.Addresses.SupplyController)
	cfg.vaultAddr = common.HexToAddress(proj.Addresses.Vault)
	cfg.escrowAddr = common.HexToAddress(proj.Addresses.RedemptionEscrow)

	if !strings.EqualFold(proj.Addresses.QuoteToken, cfg.quoteAddr.Hex()) {
		return fmt.Errorf("GET /api/v1/project: quoteToken = %q, want the deployed %q", proj.Addresses.QuoteToken, cfg.quoteAddr.Hex())
	}
	if !strings.EqualFold(proj.Auditor, cfg.auditorAddr.Hex()) {
		return fmt.Errorf("GET /api/v1/project: auditor = %q, want %q", proj.Auditor, cfg.auditorAddr.Hex())
	}
	if proj.ChainID != cfg.chainID.Int64() {
		return fmt.Errorf("GET /api/v1/project: chainId = %d, want %d", proj.ChainID, cfg.chainID.Int64())
	}
	if proj.Decimals != 18 {
		return fmt.Errorf("GET /api/v1/project: decimals = %d, want 18", proj.Decimals)
	}

	// The server wires its address-dependent services (records/sales/
	// redemptions/compliance-status) asynchronously once the project reaches
	// Active (cmd/platform watchProject rebuilds the app + router). Wait
	// until a service-gated endpoint stops reporting 501 so the subsequent
	// lifecycle steps don't race that rebuild.
	if err := poll("address-dependent services wired after activation", 60*time.Second, 2*time.Second, func() (bool, error) {
		_, err := api.do(http.MethodGet, "/api/v1/sales/inventory", "", nil, "")
		return err == nil, nil
	}); err != nil {
		return fmt.Errorf("services not wired after activation: %w", err)
	}

	fmt.Printf("    project Active: token=%s vault=%s supplyController=%s escrow=%s\n",
		cfg.tokenAddr.Hex(), cfg.vaultAddr.Hex(), cfg.supplyCtrlAddr.Hex(), cfg.escrowAddr.Hex())
	if proj.VerificationNote != "" {
		fmt.Printf("    (verification note: %s)\n", proj.VerificationNote)
	}
	return nil
}

func walletOwnership(api *apiClient, key *ecdsa.PrivateKey, addr common.Address) error {
	var ch struct{ Address, Nonce, Message, ExpiresAt string }
	if err := api.postJSON("/api/v1/compliance/challenge", "", map[string]string{"address": addr.Hex()}, &ch, ""); err != nil {
		return err
	}
	sig, err := signPersonal(key, ch.Message)
	if err != nil {
		return err
	}
	var ws walletStatus
	if err := api.postJSON("/api/v1/compliance/challenge/verify", "", map[string]string{
		"address": addr.Hex(), "nonce": ch.Nonce, "signature": "0x" + hex.EncodeToString(sig),
	}, &ws, ""); err != nil {
		return err
	}
	if !ws.OwnershipVerified {
		return fmt.Errorf("challenge/verify did not report ownershipVerified=true")
	}
	return nil
}

type walletStatus struct {
	Address           string `json:"address"`
	Status            string `json:"status"`
	ValidUntil        int64  `json:"validUntil"`
	OwnershipVerified bool   `json:"ownershipVerified"`
}

func allowInvestor(api *apiClient, cfg config, investor common.Address) error {
	var tx struct{ TxHash, Status, IdempotencyKey string }
	if err := api.postJSON("/api/v1/compliance/status", cfg.adminBearer, map[string]any{
		"address": investor.Hex(), "status": "Allowed", "validUntil": 0,
	}, &tx, "e2e-allow-investor"); err != nil {
		return err
	}
	return poll("investor Allowed in wallet read model", 60*time.Second, 2*time.Second, func() (bool, error) {
		var wallets []walletStatus
		// wallet/KYC history is operator/admin-gated.
		if err := api.getJSON("/api/v1/compliance/wallets", cfg.adminBearer, &wallets); err != nil {
			return false, err
		}
		for _, w := range wallets {
			if strings.EqualFold(w.Address, investor.Hex()) && w.Status == "Allowed" {
				return true, nil
			}
		}
		return false, nil
	})
}

// validateProfile checks the profile via the pure POST /api/v1/profile/validate
// (it has no persistence side effect), then
// persists it via the admin-only, create-once POST /api/v1/profile — the
// only endpoint that durably stores an Asset Profile now.
func validateProfile(api *apiClient, cfg config) (string, error) {
	raw, err := os.ReadFile(cfg.workDir + "/profile.json")
	if err != nil {
		return "", err
	}
	var result struct {
		Valid         bool     `json:"valid"`
		Errors        []string `json:"errors"`
		ProfileDigest string   `json:"profileDigest"`
		CID           string   `json:"cid"`
	}
	raw2, err := api.do(http.MethodPost, "/api/v1/profile/validate", "", raw, "")
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw2, &result); err != nil {
		return "", err
	}
	if !result.Valid {
		return "", fmt.Errorf("profile invalid: %v", result.Errors)
	}

	var created struct {
		Valid         bool     `json:"valid"`
		Errors        []string `json:"errors"`
		ProfileDigest string   `json:"profileDigest"`
	}
	if err := api.postJSON("/api/v1/profile", cfg.adminBearer, json.RawMessage(raw), &created, "e2e-create-profile"); err != nil {
		return "", fmt.Errorf("profile create (admin): %w", err)
	}
	if !created.Valid {
		return "", fmt.Errorf("profile create (admin) reported invalid: %v", created.Errors)
	}
	return result.ProfileDigest, nil
}

func createRecord(api *apiClient, cfg config) (string, error) {
	body := map[string]any{
		"recordId": "E2E-REC-1",
		"asset":    map[string]any{"barId": "BAR-001", "weightGrams": 1000},
		"amount":   amount1000.String(),
	}
	var rec struct {
		RecordID string `json:"recordId"`
		Status   string `json:"status"`
	}
	if err := api.postJSON("/api/v1/assets/records", cfg.adminBearer, body, &rec, "e2e-create-record"); err != nil {
		return "", err
	}
	if rec.Status != "Pending" {
		return "", fmt.Errorf("expected new record status Pending, got %q", rec.Status)
	}
	return rec.RecordID, nil
}

func downloadPackage(api *apiClient, cfg config, recordID string) (string, error) {
	// package download is operator/admin-gated.
	raw, err := api.do(http.MethodGet, "/api/v1/assets/records/"+recordID+"/package", cfg.adminBearer, nil, "")
	if err != nil {
		return "", err
	}
	path := cfg.workDir + "/" + recordID + ".rwa"
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func signPackage(cfg config, pkgPath, profileDigest string) (string, error) {
	outPath := cfg.workDir + "/signed-result.json"
	// The signer binds chain/controller/vault/auditor/projectId/profileDigest
	// via a REQUIRED --policy trust-root file, not CLI flags. Write
	// one from the deployed stack the harness observed. Addresses are compared
	// case-insensitively; chainId is a decimal string; projectId is the UUID
	// verbatim; maxAttestationLifetimeHours is omitted to use the signer default.
	policyPath := cfg.workDir + "/signer-policy.json"
	policy := map[string]string{
		"chainId":       cfg.chainID.String(),
		"controller":    cfg.supplyCtrlAddr.Hex(),
		"vault":         cfg.vaultAddr.Hex(),
		"auditor":       cfg.auditorAddr.Hex(),
		"projectId":     cfg.projectUUID,
		"profileDigest": profileDigest,
	}
	policyJSON, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal signer policy: %w", err)
	}
	if err := os.WriteFile(policyPath, policyJSON, 0o600); err != nil {
		return "", fmt.Errorf("write signer policy: %w", err)
	}
	args := []string{
		"sign", pkgPath,
		"--keystore", cfg.keystorePath,
		"--password-file", cfg.keystorePass,
		"--policy", policyPath,
		"--yes",
		"--unsafe-test-mode",
		"--out", outPath,
	}
	cmd := exec.Command(cfg.signerBin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w", string(out), err)
	}
	fmt.Print(string(out))
	return outPath, nil
}

// mintOnChain broadcasts SupplyController.mint(attestation, signature) from the
// admin wallet — the auditor-signed mint is no longer relayed by the server.
// The exact MintAttestation the auditor signed is read back from the .rwa
// package's typed-data.json (the same bytes the signer signed), and the
// signature from the signer's signed-result.json; the server then observes the
// resulting Minted event and flips the record to Minted (ReconcileMinted).
func mintOnChain(ctx context.Context, chain *chainClient, cfg config, deployerKey *ecdsa.PrivateKey, pkgPath, signedResultPath string) error {
	zipBytes, err := os.ReadFile(pkgPath)
	if err != nil {
		return fmt.Errorf("read package: %w", err)
	}
	opened, err := auditpkg.OpenPackage(zipBytes)
	if err != nil {
		return fmt.Errorf("open package: %w", err)
	}
	tdRaw, ok := opened.Files["typed-data.json"]
	if !ok {
		return fmt.Errorf("package has no typed-data.json")
	}
	var td auditpkg.TypedDataDoc
	if err := json.Unmarshal(tdRaw, &td); err != nil {
		return fmt.Errorf("parse typed-data.json: %w", err)
	}

	srRaw, err := os.ReadFile(signedResultPath)
	if err != nil {
		return fmt.Errorf("read signed-result.json: %w", err)
	}
	var sr struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(srRaw, &sr); err != nil {
		return fmt.Errorf("parse signed-result.json: %w", err)
	}

	profileDigest, err := hexToBytes32Local(td.Message.ProfileDigest)
	if err != nil {
		return fmt.Errorf("profileDigest: %w", err)
	}
	recordKey, err := hexToBytes32Local(td.Message.RecordKey)
	if err != nil {
		return fmt.Errorf("recordKey: %w", err)
	}
	metadataDigest, err := hexToBytes32Local(td.Message.MetadataDigest)
	if err != nil {
		return fmt.Errorf("metadataDigest: %w", err)
	}
	amount, ok := new(big.Int).SetString(td.Message.Amount, 10)
	if !ok {
		return fmt.Errorf("invalid amount %q", td.Message.Amount)
	}
	nonce, ok := new(big.Int).SetString(td.Message.Nonce, 10)
	if !ok {
		return fmt.Errorf("invalid nonce %q", td.Message.Nonce)
	}
	sig, err := hexBytesLocal(sr.Signature)
	if err != nil {
		return fmt.Errorf("signature: %w", err)
	}

	data, err := bindings.NewSupplyController().PackMint(bindings.MintAttestation{
		Auditor: common.HexToAddress(td.Message.Auditor), ProfileDigest: profileDigest, RecordKey: recordKey,
		MetadataDigest: metadataDigest, Amount: amount, Nonce: nonce,
		ValidUntil: uint64(td.Message.ValidUntil), Vault: common.HexToAddress(td.Message.Vault),
	}, sig)
	if err != nil {
		return fmt.Errorf("encode mint calldata: %w", err)
	}
	if _, err := chain.sendTx(ctx, deployerKey, cfg.supplyCtrlAddr, data); err != nil {
		return fmt.Errorf("broadcast SupplyController.mint: %w", err)
	}
	return nil
}

func hexToBytes32Local(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hexBytesLocal(s)
	if err != nil {
		return out, err
	}
	if len(b) != 32 {
		return out, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}

func hexBytesLocal(s string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(s, "0x"))
}

func waitRecordMinted(api *apiClient, cfg config, recordID string) error {
	return poll("record "+recordID+" status Minted", 90*time.Second, 3*time.Second, func() (bool, error) {
		var records []struct {
			RecordID string `json:"recordId"`
			Status   string `json:"status"`
		}
		// the full asset-record list is operator/admin-gated.
		if err := api.getJSON("/api/v1/assets/records", cfg.adminBearer, &records); err != nil {
			return false, err
		}
		for _, r := range records {
			if r.RecordID == recordID {
				return r.Status == "Minted", nil
			}
		}
		return false, nil
	})
}

func buy(ctx context.Context, api *apiClient, chain *chainClient, cfg config, investorKey *ecdsa.PrivateKey, investor common.Address) error {
	quoteAmount, err := chain.previewBuy(ctx, cfg.vaultAddr, amount100)
	if err != nil {
		return err
	}

	// MockERC20.mint is permissionless; the investor mints its own quote
	// tokens and approves the Vault, exactly like E2EFlow.s.sol's _buy().
	if _, err := chain.sendTx(ctx, investorKey, cfg.quoteAddr, packMint(investor, quoteAmount)); err != nil {
		return fmt.Errorf("mint quote tokens: %w", err)
	}
	if _, err := chain.sendTx(ctx, investorKey, cfg.quoteAddr, packApprove(cfg.vaultAddr, quoteAmount)); err != nil {
		return fmt.Errorf("approve vault: %w", err)
	}

	deadline := uint64(time.Now().Add(1 * time.Hour).Unix())
	data, err := bindings.NewVault().PackBuy(amount100, quoteAmount, investor, deadline)
	if err != nil {
		return err
	}
	if _, err := chain.sendTx(ctx, investorKey, cfg.vaultAddr, data); err != nil {
		return fmt.Errorf("submit Vault.buy: %w", err)
	}

	return poll("purchase visible in read model", 60*time.Second, 3*time.Second, func() (bool, error) {
		var purchases []struct {
			Buyer       string `json:"buyer"`
			TokenAmount string `json:"tokenAmount"`
		}
		// the full purchase history list is operator/admin-gated.
		if err := api.getJSON("/api/v1/sales/purchases", cfg.adminBearer, &purchases); err != nil {
			return false, err
		}
		for _, p := range purchases {
			if strings.EqualFold(p.Buyer, investor.Hex()) && p.TokenAmount == amount100.String() {
				return true, nil
			}
		}
		return false, nil
	})
}

// requestRedemption approves the RedemptionEscrow for rwaAmount and submits
// requestRedemption from the investor's wallet, decoding the new request's
// id directly from the RedemptionRequested log in the tx receipt (avoids a
// racy poll-and-diff against /redemptions to find "the new one").
func requestRedemption(ctx context.Context, api *apiClient, chain *chainClient, cfg config, investorKey *ecdsa.PrivateKey, investor common.Address, rwaAmount *big.Int) (string, error) {
	minQuoteOut, err := chain.previewRedeem(ctx, cfg.escrowAddr, rwaAmount)
	if err != nil {
		return "", err
	}

	if _, err := chain.sendTx(ctx, investorKey, cfg.tokenAddr, packApprove(cfg.escrowAddr, rwaAmount)); err != nil {
		return "", fmt.Errorf("approve escrow: %w", err)
	}

	escrow := bindings.NewRedemptionEscrow()
	deadline := uint64(time.Now().Add(1 * time.Hour).Unix())
	data, err := escrow.PackRequestRedemption(rwaAmount, minQuoteOut, deadline)
	if err != nil {
		return "", err
	}
	receipt, err := chain.sendTx(ctx, investorKey, cfg.escrowAddr, data)
	if err != nil {
		return "", fmt.Errorf("submit RedemptionEscrow.requestRedemption: %w", err)
	}

	wantTopic0 := escrow.EventID("RedemptionRequested")
	for _, l := range receipt.Logs {
		if l.Address != cfg.escrowAddr || len(l.Topics) == 0 || l.Topics[0] != wantTopic0 {
			continue
		}
		ev, err := escrow.UnpackRedemptionRequested(l.Data, l.Topics)
		if err != nil {
			return "", err
		}
		id := ev.ID.String()
		if err := poll("redemption "+id+" visible as Pending", 60*time.Second, 3*time.Second, func() (bool, error) {
			var r struct{ Status string }
			if err := api.getJSON("/api/v1/redemptions/"+id, "", &r); err != nil {
				if apiErr, ok := err.(*apiError); ok && apiErr.status == 404 {
					return false, nil
				}
				return false, err
			}
			return r.Status == "Pending", nil
		}); err != nil {
			return "", err
		}
		return id, nil
	}
	return "", fmt.Errorf("RedemptionRequested log not found in receipt")
}

func fundAndClaim(ctx context.Context, api *apiClient, chain *chainClient, cfg config, treasurerKey *ecdsa.PrivateKey, treasurer common.Address, id string) error {
	var r struct {
		QuoteAmount string `json:"quoteAmount"`
	}
	if err := api.getJSON("/api/v1/redemptions/"+id, "", &r); err != nil {
		return err
	}
	quoteAmount, ok := new(big.Int).SetString(r.QuoteAmount, 10)
	if !ok {
		return fmt.Errorf("bad quoteAmount %q", r.QuoteAmount)
	}

	// treasurer==deployer by default (Deploy.s.sol); mint+approve then fund,
	// mirroring E2EFlow.s.sol's _fundAndClaim().
	if _, err := chain.sendTx(ctx, treasurerKey, cfg.quoteAddr, packMint(treasurer, quoteAmount)); err != nil {
		return fmt.Errorf("mint quote for funding: %w", err)
	}
	if _, err := chain.sendTx(ctx, treasurerKey, cfg.quoteAddr, packApprove(cfg.escrowAddr, quoteAmount)); err != nil {
		return fmt.Errorf("approve escrow for funding: %w", err)
	}

	// Funding is now a role-gated wallet tx the treasurer submits directly
	// (no server fund-calldata endpoint); encode fundRedemption locally,
	// mirroring the web's connected-wallet flow.
	fundID, ok := new(big.Int).SetString(id, 10)
	if !ok {
		return fmt.Errorf("bad redemption id %q", id)
	}
	if _, err := chain.sendTx(ctx, treasurerKey, cfg.escrowAddr, packFundRedemption(fundID)); err != nil {
		return fmt.Errorf("submit fund calldata: %w", err)
	}

	// Mine one more block so confirmations>=CHAIN_CONFIRMATIONS(=1) and the
	// read model's `claimable` convenience flag flips true (architecture:
	// "A Pending redemption is never a payment guarantee" — same idea
	// applies to Funded-but-not-yet-confirmed).
	if err := chain.mineEmptyBlock(ctx); err != nil {
		return fmt.Errorf("mine block after funding: %w", err)
	}

	if err := poll("redemption "+id+" Funded+claimable", 90*time.Second, 3*time.Second, func() (bool, error) {
		var rr struct {
			Status    string `json:"status"`
			Claimable bool   `json:"claimable"`
		}
		if err := api.getJSON("/api/v1/redemptions/"+id, "", &rr); err != nil {
			return false, err
		}
		return rr.Status == "Funded" && rr.Claimable, nil
	}); err != nil {
		return err
	}

	claimID, ok := new(big.Int).SetString(id, 10)
	if !ok {
		return fmt.Errorf("bad redemption id %q", id)
	}
	claimData, err := bindings.NewRedemptionEscrow().PackClaimRedemption(claimID)
	if err != nil {
		return err
	}
	// claimRedemption is permissionless; any funded wallet can submit it.
	if _, err := chain.sendTx(ctx, treasurerKey, cfg.escrowAddr, claimData); err != nil {
		return fmt.Errorf("submit RedemptionEscrow.claimRedemption: %w", err)
	}

	return poll("redemption "+id+" Completed", 60*time.Second, 3*time.Second, func() (bool, error) {
		var rr struct{ Status string }
		if err := api.getJSON("/api/v1/redemptions/"+id, "", &rr); err != nil {
			return false, err
		}
		return rr.Status == "Completed", nil
	})
}

func timeoutAndCancel(ctx context.Context, api *apiClient, chain *chainClient, cfg config, investorKey *ecdsa.PrivateKey, investor common.Address, id string) error {
	if err := chain.increaseTime(ctx, cfg.redeemTimeout+1); err != nil {
		return fmt.Errorf("evm_increaseTime: %w", err)
	}
	if err := chain.mineEmptyBlock(ctx); err != nil {
		return fmt.Errorf("evm_mine: %w", err)
	}

	cancelID, ok := new(big.Int).SetString(id, 10)
	if !ok {
		return fmt.Errorf("bad redemption id %q", id)
	}
	data, err := bindings.NewRedemptionEscrow().PackCancelRedemption(cancelID)
	if err != nil {
		return err
	}
	if _, err := chain.sendTx(ctx, investorKey, cfg.escrowAddr, data); err != nil {
		return fmt.Errorf("submit RedemptionEscrow.cancelRedemption: %w", err)
	}

	return poll("redemption "+id+" Cancelled", 60*time.Second, 3*time.Second, func() (bool, error) {
		var rr struct{ Status string }
		if err := api.getJSON("/api/v1/redemptions/"+id, "", &rr); err != nil {
			return false, err
		}
		return rr.Status == "Cancelled", nil
	})
}

// enforcementState is the admin-only GET /api/v1/project/enforcement view the
// server projects from RWAToken's Frozen and ForcedTransfer events. It is not
// on the public GET /api/v1/project: naming frozen wallets is operational
// compliance data.
type enforcementState struct {
	FrozenBalances     map[string]string `json:"frozenBalances"`
	LastForcedTransfer *struct {
		From        string `json:"from"`
		To          string `json:"to"`
		Amount      string `json:"amount"`
		TxHash      string `json:"txHash"`
		BlockNumber uint64 `json:"blockNumber"`
	} `json:"lastForcedTransfer"`
}

// frozenAmount returns the projected frozen amount for holder, case-insensitively
// (the projection checksums its keys), and whether an entry exists at all.
func (e enforcementState) frozenAmount(holder common.Address) (string, bool) {
	for addr, amount := range e.FrozenBalances {
		if strings.EqualFold(addr, holder.Hex()) {
			return amount, true
		}
	}
	return "", false
}

// enforcementLifecycle drives the ERC-7943 admin powers against the live chain
// and proves the server's read model follows: freeze part of the investor's
// balance, seize more than the unfrozen part (which reduces the freeze), then
// release what is left. Every write is a wallet transaction the admin signs;
// the server only indexes the events, so each assertion polls GET /project
// rather than reading anything back from the transaction it just sent.
//
// It runs last so the redemption assertions above are made against an
// unencumbered balance.
func enforcementLifecycle(
	ctx context.Context,
	api *apiClient,
	chain *chainClient,
	cfg config,
	adminKey *ecdsa.PrivateKey,
	adminAddr, investor common.Address,
) error {
	// The seizure destination has to be compliance-Allowed, admin or not.
	if err := allowRecoveryWallet(api, cfg, adminAddr); err != nil {
		return fmt.Errorf("allow recovery wallet: %w", err)
	}

	pollProjection := func(desc string, check func(enforcementState) bool) error {
		return poll(desc, 90*time.Second, 3*time.Second, func() (bool, error) {
			var st enforcementState
			if err := api.getJSON("/api/v1/project/enforcement", cfg.adminBearer, &st); err != nil {
				return false, err
			}
			return check(st), nil
		})
	}

	if _, err := chain.sendTx(ctx, adminKey, cfg.tokenAddr, packSetFrozenTokens(investor, amount25)); err != nil {
		return fmt.Errorf("submit setFrozenTokens: %w", err)
	}
	if err := pollProjection("25 RWA frozen against the investor in GET /project/enforcement", func(st enforcementState) bool {
		amount, ok := st.frozenAmount(investor)
		return ok && amount == amount25.String()
	}); err != nil {
		return err
	}

	// The investor holds 60 RWA here (100 bought, 40 redeemed and claimed, 10
	// escrowed and returned by the cancel), so a 25 freeze leaves 35 movable.
	// Seizing 40 therefore has to release 5 frozen tokens first, and the
	// projection must land on 20 rather than the original 25.
	if _, err := chain.sendTx(ctx, adminKey, cfg.tokenAddr, packForcedTransfer(investor, adminAddr, amount40)); err != nil {
		return fmt.Errorf("submit forcedTransfer: %w", err)
	}
	if err := pollProjection("seizure projected: freeze reduced to 20 RWA, forced transfer summarized",
		func(st enforcementState) bool {
			amount, ok := st.frozenAmount(investor)
			if !ok || amount != amount20.String() {
				return false
			}
			f := st.LastForcedTransfer
			return f != nil && f.Amount == amount40.String() &&
				strings.EqualFold(f.From, investor.Hex()) && strings.EqualFold(f.To, adminAddr.Hex()) &&
				f.TxHash != "" && f.BlockNumber > 0
		}); err != nil {
		return err
	}

	// Zero releases the hold, and the holder drops out of the map entirely
	// rather than lingering with a zero balance.
	if _, err := chain.sendTx(ctx, adminKey, cfg.tokenAddr, packSetFrozenTokens(investor, big.NewInt(0))); err != nil {
		return fmt.Errorf("submit setFrozenTokens(0): %w", err)
	}
	return pollProjection("freeze released: the investor is gone from frozenBalances",
		func(st enforcementState) bool {
			_, ok := st.frozenAmount(investor)
			return !ok && st.LastForcedTransfer != nil
		})
}

// allowRecoveryWallet marks addr Allowed so it can receive seized tokens. Same
// admin compliance endpoint as the investor onboarding above, with its own
// idempotency key.
func allowRecoveryWallet(api *apiClient, cfg config, addr common.Address) error {
	var tx struct{ TxHash, Status string }
	if err := api.postJSON("/api/v1/compliance/status", cfg.adminBearer, map[string]any{
		"address": addr.Hex(), "status": "Allowed", "validUntil": 0,
	}, &tx, "e2e-allow-recovery-wallet"); err != nil {
		return err
	}
	return poll("recovery wallet Allowed on-chain", 60*time.Second, 2*time.Second, func() (bool, error) {
		var wallets []walletStatus
		if err := api.getJSON("/api/v1/compliance/wallets", cfg.adminBearer, &wallets); err != nil {
			return false, err
		}
		for _, w := range wallets {
			if strings.EqualFold(w.Address, addr.Hex()) && w.Status == "Allowed" {
				return true, nil
			}
		}
		return false, nil
	})
}
