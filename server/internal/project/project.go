// Package project observes on-chain deployments and runs post-deployment
// verification. RWAFactory.deploy is permissionless and the admin's own wallet
// broadcasts it, so no server key takes part in deployment. The server indexes
// the factory's ProjectDeployed event and, once one that binds to the stored,
// digest-verified Asset Profile appears, adopts the resulting stack, verifies
// it and marks the project Active.
package project

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Service adopts and verifies observed factory deployments.
type Service struct {
	client      blockchain.Client
	factory     bindings.Factory
	factoryAddr common.Address
	repo        repository.ProjectRepository
	chainID     int64
	// adminAddress is the configured admin wallet, the trust anchor for
	// adoption. RWAFactory.deploy is permissionless and projectId and
	// profileDigest are public, so matching them proves only that someone read
	// the chain. A deployment is adopted only when the admin's wallet sent it.
	adminAddress common.Address

	warnUnauthenticatedOnce sync.Once
	// loggedRejections keeps a rejected deploy tx from re-logging on every
	// tick of the reconcile ticker.
	loggedRejections sync.Map
}

// New constructs a deployment-observing Service. It only reads the chain and the
// database and never signs a transaction.
//
// adminAddress is the configured admin wallet (ADMIN_ADDRESS), which binds an
// observed deployment to this operator. A zero adminAddress disables that
// binding; see adminBindingError.
func New(client blockchain.Client, factoryAddr common.Address, repo repository.ProjectRepository, chainID int64, adminAddress common.Address) *Service {
	return &Service{
		client: client, factory: bindings.NewFactory(), factoryAddr: factoryAddr,
		repo: repo, chainID: chainID, adminAddress: adminAddress,
	}
}

// GetProject returns the current project record.
func (s *Service) GetProject(ctx context.Context) (*models.Project, error) {
	return s.repo.Get(ctx)
}

// SupportedFactoryVersions is the set of RWAFactory.version() strings that this
// server's verification logic supports
var SupportedFactoryVersions = map[string]bool{"rwa-v2": true}

// ReconcileDeployment adopts and verifies an indexed ProjectDeployed event
// from the configured factory. It is safe to call repeatedly. If the event
// backing an Active or Verifying project is rolled back, demote to Undeployed.
//
// The event's projectId must equal keccak256 of the stored profile's UUID, and
// profileDigest must match that profile's digest. Both values are public, so
// also require the configured admin as transaction sender and ProjectConfig.admin
// (see adminBindingError). Verification takes roles, treasury, auditor, prices
// and quote token from that authorized transaction's calldata.
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

	// Only an admin-bound event is an adoption candidate. The search walks
	// newest first (ListByName returns (block, logIndex) ascending) and takes the
	// first authorized event, so a later look-alike deployment is excluded
	// instead of winning because it is newer.
	match, err := s.latestAuthorizedDeploy(ctx, matches)
	if err != nil {
		return err
	}

	if match == nil {
		// reorg safety: an adopted deployment has lost its backing event
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

// latestAuthorizedDeploy returns the newest event in matches whose deploy
// transaction is bound to the configured admin, or nil when none is. matches are
// already filtered to this profile's projectId and profileDigest and sorted
// ascending.
//
// An unauthorized event is skipped, not recorded. Marking it Failed would
// overwrite a legitimately Active project, which would let an attacker cause a
// denial of service. Each rejected transaction is logged once so the reconcile
// ticker does not flood the log.
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

// deployAuthorizationError reports why the deploy transaction at txHash is not
// an adoption candidate, or "" when it is one. A non-nil error is an RPC
// failure, which means the check could not be made and is not a verdict of "not
// authorized".
func (s *Service) deployAuthorizationError(ctx context.Context, txHash common.Hash) (string, error) {
	tx, _, err := s.client.TransactionByHash(ctx, txHash)
	if err != nil {
		return "", fmt.Errorf("project: fetch deploy transaction %s: %w", txHash.Hex(), err)
	}
	cfg, err := s.factory.UnpackDeployInput(tx.Data())
	if err != nil {
		return "calldata is not an RWAFactory.deploy(ProjectConfig): " + err.Error(), nil
	}
	return s.adminBindingError(tx, cfg), nil
}

// adminBindingError returns a rejection reason or "" for an authorized deploy.
// The recovered sender and ProjectConfig.admin must both equal the configured
// admin, preventing adoption of a profile-matching deployment sent by another
// wallet or naming another admin.
//
// With no configured admin, this check permits profile-matching deployments
// and logs one warning. Production configuration requires security.admin_address.
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

// demoteOrphaned follows the orphan demotion in assets.RecordService.ReconcileMinted.
// A project whose adopting ProjectDeployed event is no longer canonical is reset
// to Undeployed with its chain-derived fields cleared, so a later deployment can
// be adopted cleanly and no service keeps using rolled-back addresses.
func (s *Service) demoteOrphaned(ctx context.Context, p *models.Project) (err error) {
	p.Status = models.ProjectStatusUndeployed
	p.Addresses = models.Addresses{}
	p.Version = ""
	p.BytecodeVerified = false
	p.Roles = nil
	p.DeployTxHash = ""
	p.Admin, p.Auditor, p.Treasury, p.RedemptionManager = "", "", "", ""
	p.ComplianceOperator, p.Pricer, p.Treasurer, p.QuoteToken = "", "", "", ""
	p.PurchasePricePerWholeToken, p.RedemptionPricePerWholeToken = "", ""
	p.QuoteDecimals = 0
	p.VerificationNote = "deployment demoted: the ProjectDeployed event it was adopted from is no longer canonical (reorg)"
	p.UpdatedAt = time.Now().UTC()
	if err := s.repo.Upsert(ctx, p); err != nil {
		return fmt.Errorf("project: demote orphaned deployment: %w", err)
	}
	return nil
}

// VerifyDeployment adopts the ProjectDeployed transaction at txHash and runs the
// full post-deploy verification pass. The expected projectId, profileDigest and
// decimals come from the stored, digest-verified profile. Every other expected
// value (role holders, treasury, auditor, prices, redemptionTimeout, quoteToken)
// is recovered from the deploy transaction's input calldata, the ProjectConfig
// the admin's wallet signed, and cross-checked against on-chain state. A failed
// check marks the project Failed with the reason recorded. Only an RPC or
// decoding error returns a non-nil Go error.
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
		if len(lg.Topics) == 0 || lg.Topics[0] != s.factory.EventID("ProjectDeployed") {
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

	// Recover the intended ProjectConfig from the deploy transaction's calldata.
	// The ProjectDeployed event carries none of the role, treasury, auditor or
	// price configuration, so the expected values come from the bytes the admin's
	// wallet signed.
	tx, _, err := s.client.TransactionByHash(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("project: fetch deploy transaction: %w", err)
	}
	cfg, err := s.factory.UnpackDeployInput(tx.Data())
	if err != nil {
		return s.markVerificationFailed(ctx, p, "deploy transaction calldata could not be decoded to an RWAFactory.deploy(ProjectConfig): "+err.Error())
	}
	// Cross-check the signed calldata against the profile: the deployer must have
	// submitted this profile's identity and decimals, not different ones
	if cfg.ProjectID != common.Hash(wantProjectID) {
		return s.markVerificationFailed(ctx, p, "deploy calldata projectId disagrees with the stored asset profile")
	}
	if cfg.ProfileDigest != common.Hash(wantProfileDigest) {
		return s.markVerificationFailed(ctx, p, "deploy calldata profileDigest disagrees with the stored asset profile")
	}
	if cfg.Decimals != profile.TokenDecimals {
		return s.markVerificationFailed(ctx, p, fmt.Sprintf("deploy calldata decimals %d disagrees with the stored asset profile's %d", cfg.Decimals, profile.TokenDecimals))
	}
	// The calldata is a trustworthy allowlist only if this operator authorized the
	// transaction that carried it, because otherwise every check below confirms
	// that an attacker's stack matches the attacker's own config.
	// ReconcileDeployment already filters unauthorized events, so this repeats the
	// check for direct callers.
	if reason := s.adminBindingError(tx, cfg); reason != "" {
		return s.markVerificationFailed(ctx, p, reason)
	}
	applyConfig(p, cfg)

	p.Addresses = models.Addresses{
		Token: deployed.Token.Hex(), Compliance: deployed.Compliance.Hex(),
		SupplyController: deployed.SupplyController.Hex(), Vault: deployed.Vault.Hex(),
		RedemptionEscrow: deployed.RedemptionEscrow.Hex(), Strategy: deployed.Strategy.Hex(),
		QuoteToken: cfg.QuoteToken.Hex(),
	}
	p.Version = deployed.Version

	// The event comes from the contract being verified, so trust its version only
	// if the live factory.version() agrees
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

	factoryRoles, err := verifyNoFactoryRoles(ctx, s.client, s.factoryAddr, p.Addresses)
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

	// Persist the quote token's ERC-20 decimals, best effort: a read failure does
	// not fail an otherwise good deployment
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

// applyConfig copies the signed ProjectConfig's role, treasury, auditor, price
// and timeout values onto the project record, so verifyRoles and
// verifyImmutableConfig compare on-chain state with what the admin's wallet
// submitted
func applyConfig(p *models.Project, cfg bindings.ProjectConfig) {
	p.Admin = cfg.Admin.Hex()
	p.Auditor = cfg.Auditor.Hex()
	p.ComplianceOperator = cfg.ComplianceOperator.Hex()
	p.Pricer = cfg.Pricer.Hex()
	p.Treasurer = cfg.Treasurer.Hex()
	p.RedemptionManager = cfg.RedemptionManager.Hex()
	p.Treasury = cfg.Treasury.Hex()
	p.QuoteToken = cfg.QuoteToken.Hex()
	if cfg.PurchasePricePerWholeToken != nil {
		p.PurchasePricePerWholeToken = cfg.PurchasePricePerWholeToken.String()
	}
	if cfg.RedemptionPricePerWholeToken != nil {
		p.RedemptionPricePerWholeToken = cfg.RedemptionPricePerWholeToken.String()
	}
	p.RedemptionTimeout = int64(cfg.RedemptionTimeout)
}

// markVerificationFailed persists p as Failed with reason recorded and returns p
// with a nil error
func (s *Service) markVerificationFailed(ctx context.Context, p *models.Project, reason string) (*models.Project, error) {
	p.Status = models.ProjectStatusFailed
	p.VerificationNote = reason
	p.UpdatedAt = time.Now().UTC()
	if err := s.repo.Upsert(ctx, p); err != nil {
		return nil, fmt.Errorf("project: persist verification failure: %w", err)
	}
	return p, nil
}
