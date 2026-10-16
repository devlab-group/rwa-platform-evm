// Package project adopts indexed RWAFactory.ProjectDeployed events matching the stored,
// digest-verified Asset Profile, then verifies the stack before marking it Active.
// Deployment is permissionless and wallet-submitted; the server never signs it.
// Configured-admin authorization prevents adoption of look-alike deployments; see
// adminBindingError for the development bypass.
package project

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Service adopts and verifies observed factory deployments
type Service struct {
	client      blockchain.Client
	factory     bindings.Factory
	factoryAddr common.Address
	repo        repository.ProjectRepository
	chainID     int64
	// factoryVersion caches the configured factory's version() once it has been read
	factoryVersion atomic.Value
	// versionFailedAt is the unix-nano time of the last failed version() read, 0 if none
	versionFailedAt atomic.Int64
	// now is the clock of the failed-read backoff; nil means time.Now
	now func() time.Time
	// adminAddress binds adoption to the configured wallet when nonzero; public
	// projectId/profileDigest values alone cannot authenticate a permissionless deployment
	adminAddress common.Address

	warnUnauthenticatedOnce sync.Once
	// loggedRejections logs each rejected deploy transaction once per Service
	loggedRejections sync.Map
}

// New constructs a Service that reads the chain and persists project state without
// signing transactions. adminAddress (ADMIN_ADDRESS) binds adoption to this operator;
// zero disables the binding (see adminBindingError).
func New(client blockchain.Client, factoryAddr common.Address, repo repository.ProjectRepository, chainID int64, adminAddress common.Address) *Service {
	return &Service{
		client: client, factory: bindings.NewFactory(), factoryAddr: factoryAddr,
		repo: repo, chainID: chainID, adminAddress: adminAddress,
	}
}

// versionRetryAfter is how long a failed version() read is not retried
const versionRetryAfter = 30 * time.Second

// versionReadTimeout bounds one version() read
const versionReadTimeout = 3 * time.Second

// FactoryVersion caches the configured factory's first nonempty version() result.
// Missing configuration or failed/empty reads return "", with failures backed off for
// versionRetryAfter to bound public-endpoint RPC traffic.
func (s *Service) FactoryVersion(ctx context.Context) string {
	if v, ok := s.factoryVersion.Load().(string); ok {
		return v
	}
	if s.factoryAddr == (common.Address{}) {
		return ""
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	if failed := s.versionFailedAt.Load(); failed != 0 && now().Sub(time.Unix(0, failed)) < versionRetryAfter {
		return ""
	}
	// The read runs detached from the caller, so a client that disconnects from the public
	// GET /config does not record a failure that every caller then sees
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), versionReadTimeout)
	defer cancel()
	v, err := viewString(readCtx, s.client, s.factory.ABI, s.factoryAddr, "version")
	if err != nil || v == "" {
		s.versionFailedAt.Store(now().UnixNano())
		return ""
	}
	s.factoryVersion.Store(v)
	return v
}

// GetProject returns the current project record
func (s *Service) GetProject(ctx context.Context) (*models.Project, error) {
	return s.repo.Get(ctx)
}

// SupportedFactoryVersions is the set of RWAFactory.version() strings that this
// server's verification logic supports
var SupportedFactoryVersions = map[string]bool{bindings.FactoryVersionV2: true, bindings.FactoryVersionV3: true}

// ReconcileDeployment repeatedly adopts and verifies the newest authorized
// ProjectDeployed event from the configured factory. Its projectId must equal keccak256
// of the stored profile's UUID, and profileDigest must match the stored digest.
// Authorization follows adminBindingError; VerifyDeployment checks the signed calldata
// configuration.
//
// With no stored profile, skip adoption. If no authorized match survives, demote an
// Active or Verifying project to Undeployed.
func (s *Service) ReconcileDeployment(ctx context.Context, chainEvents repository.ChainEventRepository, profiles repository.AssetProfileRepository) error {
	current, err := s.repo.Get(ctx)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	if errors.Is(err, repository.ErrNotFound) {
		current = nil
	}

	profile, perr := profiles.GetCurrent(ctx)
	if errors.Is(perr, repository.ErrNotFound) {
		return nil // no profile yet, so nothing to bind or adopt
	} else if perr != nil {
		return fmt.Errorf("project: load stored asset profile: %w", perr)
	}

	wantProjectID, err := projectIDToBytes32(profile.ProjectID)
	if err != nil {
		return err
	}
	wantDigest, err := hexToBytes32(profile.Digest)
	if err != nil {
		return err
	}
	wantProjectIDHex := common.Hash(wantProjectID).Hex()
	wantDigestHex := common.Hash(wantDigest).Hex()

	events, err := chainEvents.ListByName(ctx, s.chainID, s.factoryAddr.Hex(), "ProjectDeployed")
	if err != nil {
		return fmt.Errorf("project: list ProjectDeployed events: %w", err)
	}
	var matches []*models.ChainEvent
	for _, e := range events {
		if e.Removed {
			continue
		}
		pid, _ := e.Data["projectId"].(string)
		pd, _ := e.Data["profileDigest"].(string)
		if strings.EqualFold(pid, wantProjectIDHex) && strings.EqualFold(pd, wantDigestHex) {
			matches = append(matches, e)
		}
	}

	// Search newest first; a newer unauthorized look-alike must not displace an authorized
	// deployment
	match, err := s.latestAuthorizedDeploy(ctx, matches)
	if err != nil {
		return err
	}

	if match == nil {
		// No authorized backing event survives
		if current != nil && (current.Status == models.ProjectStatusActive || current.Status == models.ProjectStatusVerifying) {
			return s.demoteOrphaned(ctx, current)
		}
		return nil
	}

	// idempotent no-op: already Active and adopted from this exact deploy tx
	if current != nil && current.Status == models.ProjectStatusActive && strings.EqualFold(current.DeployTxHash, match.TxHash) {
		return nil
	}

	_, err = s.VerifyDeployment(ctx, common.HexToHash(match.TxHash), profile)
	return err
}

// latestAuthorizedDeploy scans profile-matching events in reverse (block, logIndex)
// order, returning the newest admin-authorized event or nil. RPC errors abort the
// search.
//
// Skip unauthorized events without marking the project Failed, which would let an
// attacker disrupt an Active project. Log each rejected transaction once per Service.
func (s *Service) latestAuthorizedDeploy(ctx context.Context, matches []*models.ChainEvent) (*models.ChainEvent, error) {
	for i := len(matches) - 1; i >= 0; i-- {
		e := matches[i]
		reason, err := s.deployAuthorizationError(ctx, common.HexToHash(e.TxHash))
		if err != nil {
			return nil, err
		}
		if reason == "" {
			return e, nil
		}
		if _, seen := s.loggedRejections.LoadOrStore(strings.ToLower(e.TxHash), true); !seen {
			log.Printf("project: ignoring ProjectDeployed in tx %s - it matches this profile but is not authorized by the configured admin: %s", e.TxHash, reason)
		}
	}
	return nil, nil
}

// deployAuthorizationError returns a rejection reason or "" for an authorized txHash. A
// non-nil error means RPC failure, not an authorization verdict.
func (s *Service) deployAuthorizationError(ctx context.Context, txHash common.Hash) (string, error) {
	tx, _, err := s.client.TransactionByHash(ctx, txHash)
	if err != nil {
		return "", fmt.Errorf("project: fetch deploy transaction %s: %w", txHash.Hex(), err)
	}
	cfg, err := s.factory.UnpackDeployInput(tx.Data())
	if err != nil {
		return "calldata is not an RWAFactory.deploy(ProjectConfig): " + err.Error(), nil
	}
	return s.adminBindingError(tx, cfg.ProjectConfig), nil
}

// adminBindingError returns "" only if the recovered sender and ProjectConfig.admin
// both match the configured admin, otherwise a rejection reason.
//
// A zero configured admin permits profile-matching deployments and warns once per
// Service. Production requires security.admin_address.
func (s *Service) adminBindingError(tx *types.Transaction, cfg bindings.ProjectConfig) string {
	if s.adminAddress == (common.Address{}) {
		s.warnUnauthenticatedOnce.Do(func() {
			log.Print("project: WARNING - no admin address configured, so deployment adoption is unauthenticated: any wallet that deploys a stack matching this profile's projectId/profileDigest will be adopted. Set ADMIN_ADDRESS.")
		})
		return ""
	}
	sender, err := types.Sender(types.LatestSignerForChainID(big.NewInt(s.chainID)), tx)
	if err != nil {
		return "deploy transaction sender could not be recovered: " + err.Error()
	}
	if sender != s.adminAddress {
		return fmt.Sprintf("deploy transaction was sent by %s, not the configured admin %s", sender.Hex(), s.adminAddress.Hex())
	}
	if cfg.Admin != s.adminAddress {
		return fmt.Sprintf("deploy calldata names %s as admin, not the configured admin %s", cfg.Admin.Hex(), s.adminAddress.Hex())
	}
	return ""
}

// demoteOrphaned clears addresses, version, DeployTxHash, role holders, auditor,
// treasury, quote token and prices, resets verification and persists Undeployed for
// later adoption. Security and RedemptionTimeout remain.
func (s *Service) demoteOrphaned(ctx context.Context, p *models.Project) (err error) {
	p.Status = models.ProjectStatusUndeployed
	p.Addresses = models.Addresses{}
	p.Version = ""
	p.BytecodeVerified = false
	p.Roles = nil
	p.DeployTxHash = ""
	p.Admin, p.Auditor, p.Treasury, p.RedemptionManager = "", "", "", ""
	p.ComplianceOperator, p.Pricer, p.Treasurer, p.QuoteToken = "", "", "", ""
	p.IncomeManager, p.MinPurchaseQuote, p.StrategyKind, p.Oracle = "", "", "", nil
	p.PurchasePricePerWholeToken, p.RedemptionPricePerWholeToken = "", ""
	p.QuoteDecimals = 0
	p.VerificationNote = "deployment demoted: the ProjectDeployed event it was adopted from is no longer canonical (reorg)"
	p.UpdatedAt = time.Now().UTC()
	if err := s.repo.Upsert(ctx, p); err != nil {
		return fmt.Errorf("project: demote orphaned deployment: %w", err)
	}
	return nil
}

// VerifyDeployment adopts txHash's ProjectDeployed event and verifies its stack. The
// supplied profile provides expected projectId, profileDigest and decimals; signed
// deploy calldata supplies role holders, treasury, auditor, prices, redemptionTimeout
// and quoteToken for on-chain checks.
//
// Failed checks persist Failed with a reason. RPC, decoding or storage failures may
// return Go errors; malformed deploy calldata instead records a failed check.
func (s *Service) VerifyDeployment(ctx context.Context, txHash common.Hash, profile *models.AssetProfile) (*models.Project, error) {
	receipt, err := s.client.TransactionReceipt(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("project: fetch deploy receipt: %w", err)
	}

	now := time.Now().UTC()
	p := &models.Project{
		ProjectID: profile.ProjectID, ChainID: s.chainID, ProfileDigest: profile.Digest,
		TokenUnit: profile.TokenUnit, TokenDecimals: profile.TokenDecimals,
		DeployTxHash: txHash.Hex(),
		Status:       models.ProjectStatusVerifying, CreatedAt: now, UpdatedAt: now,
	}
	if existing, gerr := s.repo.Get(ctx); gerr == nil && !existing.CreatedAt.IsZero() {
		p.CreatedAt = existing.CreatedAt
	}

	if receipt.Status != types.ReceiptStatusSuccessful {
		return s.markVerificationFailed(ctx, p, "deploy transaction reverted")
	}

	var deployed *bindings.ProjectDeployedEvent
	for _, lg := range receipt.Logs {
		if lg.Address != s.factoryAddr {
			continue // the log must be emitted by the configured factory
		}
		if len(lg.Topics) == 0 || !s.factory.IsProjectDeployed(lg.Topics[0]) {
			continue
		}
		ev, err := s.factory.UnpackProjectDeployed(lg.Data, lg.Topics)
		if err != nil {
			return nil, fmt.Errorf("project: decode ProjectDeployed: %w", err)
		}
		deployed = &ev
		break
	}
	if deployed == nil {
		return s.markVerificationFailed(ctx, p, "deploy transaction receipt has no ProjectDeployed log emitted by the configured factory")
	}

	wantProjectID, err := projectIDToBytes32(profile.ProjectID)
	if err != nil {
		return nil, err
	}
	wantProfileDigest, err := hexToBytes32(profile.Digest)
	if err != nil {
		return nil, err
	}
	if deployed.ProjectID != common.Hash(wantProjectID) || deployed.ProfileDigest != common.Hash(wantProfileDigest) {
		return s.markVerificationFailed(ctx, p, "ProjectDeployed event projectId/profileDigest does not match the stored asset profile")
	}
	if !SupportedFactoryVersions[deployed.Version] {
		return s.markVerificationFailed(ctx, p, fmt.Sprintf("factory reported unsupported version %q", deployed.Version))
	}

	// Recover configuration from signed calldata; ProjectDeployed omits roles, treasury,
	// auditor and prices
	tx, _, err := s.client.TransactionByHash(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("project: fetch deploy transaction: %w", err)
	}
	cfg, err := s.factory.UnpackDeployInput(tx.Data())
	if err != nil {
		return s.markVerificationFailed(ctx, p, "deploy transaction calldata could not be decoded to an RWAFactory.deploy(ProjectConfig): "+err.Error())
	}
	if cfg.Version != deployed.Version || cfg.Version != deployed.Layout {
		return s.markVerificationFailed(ctx, p, fmt.Sprintf("deploy calldata is %s, the ProjectDeployed event uses the %s layout and reports version %q", cfg.Version, deployed.Layout, deployed.Version))
	}
	// Check the signed profile identity and decimals against the stored profile
	if cfg.ProjectID != common.Hash(wantProjectID) {
		return s.markVerificationFailed(ctx, p, "deploy calldata projectId disagrees with the stored asset profile")
	}
	if cfg.ProfileDigest != common.Hash(wantProfileDigest) {
		return s.markVerificationFailed(ctx, p, "deploy calldata profileDigest disagrees with the stored asset profile")
	}
	if cfg.Decimals != profile.TokenDecimals {
		return s.markVerificationFailed(ctx, p, fmt.Sprintf("deploy calldata decimals %d disagrees with the stored asset profile's %d", cfg.Decimals, profile.TokenDecimals))
	}
	// Recheck authorization for direct callers: an attacker's own calldata cannot serve as
	// a trusted allowlist
	if reason := s.adminBindingError(tx, cfg.ProjectConfig); reason != "" {
		return s.markVerificationFailed(ctx, p, reason)
	}
	applyConfig(p, cfg)

	p.Addresses = models.Addresses{
		Token: deployed.Token.Hex(), Compliance: deployed.Compliance.Hex(),
		SupplyController: deployed.SupplyController.Hex(), Vault: deployed.Vault.Hex(),
		RedemptionEscrow: deployed.RedemptionEscrow.Hex(), Strategy: deployed.Strategy.Hex(),
		QuoteToken: cfg.QuoteToken.Hex(),
	}
	if deployed.Version == bindings.FactoryVersionV3 {
		p.Addresses.IncomeDistributor = deployed.IncomeDistributor.Hex()
	}
	p.Version = deployed.Version

	// Cross-check the event's version against live factory.version()
	liveVersion, err := viewString(ctx, s.client, s.factory.ABI, s.factoryAddr, "version")
	if err != nil {
		return nil, fmt.Errorf("project: read factory.version(): %w", err)
	}
	if liveVersion != deployed.Version {
		return s.markVerificationFailed(ctx, p, fmt.Sprintf("factory.version() = %q disagrees with the ProjectDeployed event's %q", liveVersion, deployed.Version))
	}

	var reasons []string

	bytecodeOK, err := verifyBytecode(ctx, s.client, p.Addresses)
	if err != nil {
		return nil, fmt.Errorf("project: verify bytecode: %w", err)
	}
	p.BytecodeVerified = bytecodeOK
	if !bytecodeOK {
		reasons = append(reasons, "one or more deployed addresses has no on-chain code")
	}

	roles, missingRoles, unexpectedRoles, err := verifyRoles(ctx, s.client, p)
	if err != nil {
		return nil, fmt.Errorf("project: verify roles: %w", err)
	}
	p.Roles = roles
	if len(missingRoles) > 0 {
		reasons = append(reasons, fmt.Sprintf("missing required roles: %v", missingRoles))
	}
	if len(unexpectedRoles) > 0 {
		reasons = append(reasons, fmt.Sprintf("unexpected role holders: %v", unexpectedRoles))
	}

	factoryRoles, err := verifyNoFactoryRoles(ctx, s.client, s.factoryAddr, p)
	if err != nil {
		return nil, fmt.Errorf("project: verify factory role absence: %w", err)
	}
	if len(factoryRoles) > 0 {
		reasons = append(reasons, "factory retains roles on deployed contracts: "+strings.Join(factoryRoles, ", "))
	}

	allowlisted, err := verifySystemAllowlist(ctx, s.client, p.Addresses)
	if err != nil {
		return nil, fmt.Errorf("project: verify system allowlist: %w", err)
	}
	if !allowlisted {
		reasons = append(reasons, "Vault and/or RedemptionEscrow is not Allowed in the compliance registry")
	}

	configOK, configReason, err := verifyImmutableConfig(ctx, s.client, p, common.HexToAddress(p.Addresses.Strategy))
	if err != nil {
		return nil, fmt.Errorf("project: verify immutable config: %w", err)
	}
	if !configOK {
		reasons = append(reasons, configReason)
	}

	if len(reasons) > 0 {
		return s.markVerificationFailed(ctx, p, strings.Join(reasons, "; "))
	}

	// Read quote-token ERC-20 decimals best effort; failure does not reject the deployment
	if p.Addresses.QuoteToken != "" {
		if dec, derr := viewUint8(ctx, s.client, bindings.NewERC20().ABI, common.HexToAddress(p.Addresses.QuoteToken), "decimals"); derr != nil {
			log.Printf("project: read quote token decimals(%s) failed, leaving quoteDecimals unset: %v", p.Addresses.QuoteToken, derr)
		} else {
			p.QuoteDecimals = dec
		}
	}

	p.Status = models.ProjectStatusActive
	p.VerificationNote = ""
	p.UpdatedAt = time.Now().UTC()
	if err := s.repo.Upsert(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// applyConfig copies signed roles, treasury, auditor, quote token, prices, timeout and
// the rwa-v3 minimum purchase, income manager and strategy configuration onto p for
// verifyRoles and verifyImmutableConfig. An Oracle strategy has no stored prices.
func applyConfig(p *models.Project, cfg bindings.DeployCall) {
	p.Admin = cfg.Admin.Hex()
	p.Auditor = cfg.Auditor.Hex()
	p.ComplianceOperator = cfg.ComplianceOperator.Hex()
	p.Pricer = cfg.Pricer.Hex()
	p.Treasurer = cfg.Treasurer.Hex()
	p.RedemptionManager = cfg.RedemptionManager.Hex()
	p.Treasury = cfg.Treasury.Hex()
	p.QuoteToken = cfg.QuoteToken.Hex()
	p.RedemptionTimeout = int64(cfg.RedemptionTimeout)
	p.StrategyKind = models.StrategyKindFixed
	if cfg.Version == bindings.FactoryVersionV3 {
		p.IncomeManager = cfg.IncomeManager.Hex()
		p.MinPurchaseQuote = "0"
		if cfg.MinPurchaseQuote != nil {
			p.MinPurchaseQuote = cfg.MinPurchaseQuote.String()
		}
		if cfg.StrategyKind == bindings.StrategyKindOracle {
			p.StrategyKind = models.StrategyKindOracle
			p.Oracle = &models.OracleConfig{
				Feed: cfg.PriceFeed.Hex(), MaxAge: cfg.PriceMaxAge,
				PurchaseSpreadBps: cfg.PurchaseSpreadBps, RedemptionSpreadBps: cfg.RedemptionSpreadBps,
				SequencerUptimeFeed: cfg.SequencerUptimeFeed.Hex(), GracePeriod: cfg.SequencerGracePeriod,
			}
			return
		}
	}
	if cfg.PurchasePricePerWholeToken != nil {
		p.PurchasePricePerWholeToken = cfg.PurchasePricePerWholeToken.String()
	}
	if cfg.RedemptionPricePerWholeToken != nil {
		p.RedemptionPricePerWholeToken = cfg.RedemptionPricePerWholeToken.String()
	}
}

// markVerificationFailed persists p as Failed with reason, returning (p, nil) if the
// write succeeds
func (s *Service) markVerificationFailed(ctx context.Context, p *models.Project, reason string) (*models.Project, error) {
	p.Status = models.ProjectStatusFailed
	p.VerificationNote = reason
	p.UpdatedAt = time.Now().UTC()
	if err := s.repo.Upsert(ctx, p); err != nil {
		return nil, fmt.Errorf("project: persist verification failure: %w", err)
	}
	return p, nil
}
