// Package project OBSERVES on-chain deployments and runs post-deployment
// verification. The server no longer submits the factory transaction from a
// hot key: RWAFactory.deploy is permissionless and
// is broadcast from the admin's own wallet. The server instead indexes the
// factory's ProjectDeployed event and, once one that binds to the stored
// (digest-verified) Asset Profile appears, adopts and fully verifies the
// resulting stack before marking the project Active — so no server key is
// part of deployment at all.
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
	// adminAddress is the configured operator wallet — the trust anchor for
	// adoption. RWAFactory.deploy is permissionless and projectId/profileDigest
	// are both public, so matching them proves only that someone read the
	// chain. A deployment is adopted only when the admin's own wallet sent it.
	adminAddress common.Address

	warnUnauthenticatedOnce sync.Once
	// loggedRejections keeps a rejected deploy tx from re-logging on every
	// tick of the reconcile ticker.
	loggedRejections sync.Map
}

// New constructs a deployment-observing Service. The server holds no deployer
// key: it only reads the chain and the DB, never signs a factory transaction.
//
// adminAddress is the configured admin wallet (config ADMIN_ADDRESS) and is
// what binds an observed deployment to this operator. A zero adminAddress
// disables that binding — see adminBindingError.
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

// SupportedFactoryVersions is the exact set of RWAFactory.version() strings
// this server's verification logic understands.
var SupportedFactoryVersions = map[string]bool{"rwa-v2": true}

// ReconcileDeployment is the reorg-safe deployment projector that replaces the
// former server-broadcast-plus-reconciler flow. It looks for a ProjectDeployed
// event (indexed from the configured factory) that binds to the stored,
// digest-verified Asset Profile and, when one appears, adopts and fully
// verifies the resulting stack; if a project previously reached Active/
// Verifying via an event that is no longer canonical (rolled back by a reorg),
// it demotes the project back to Undeployed. Safe to call repeatedly on a
// ticker.
//
// BINDING: a ProjectDeployed event is adopted ONLY when its projectId AND
// profileDigest match the stored profile — projectId is
// keccak256(the profile's UUID projectId), profileDigest is the profile's own
// digest — so the on-chain token can never be bound to a profile the server
// will not validate every record/mint against.
//
// AUTHORIZATION: that binding alone is not enough, because both values are
// public and RWAFactory.deploy is permissionless — anyone can deploy a
// look-alike stack carrying them. The deploy transaction must ALSO be signed by
// the configured admin and name that same admin in its ProjectConfig; see
// adminBindingError. Everything else the verification pass treats as expected
// (roles, treasury, auditor, prices, quote token) comes from that transaction's
// calldata, so without this check the server would be verifying an attacker's
// stack against the attacker's own configuration and finding it consistent.
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
		return nil // no profile yet — nothing to bind or adopt
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

	// Only an admin-bound event is an adoption candidate. Walking newest-first
	// and taking the first authorized one (ListByName is (block,logIndex)
	// ascending) means a later look-alike deployment is EXCLUDED rather than
	// preferred — the old "keep the latest match" rule handed the project to
	// whoever deployed most recently.
	match, err := s.latestAuthorizedDeploy(ctx, matches)
	if err != nil {
		return err
	}

	if match == nil {
		// Reorg safety: a previously-adopted deployment lost its backing event.
		if current != nil && (current.Status == models.ProjectStatusActive || current.Status == models.ProjectStatusVerifying) {
			return s.demoteOrphaned(ctx, current)
		}
		return nil
	}

	// Idempotent no-op: already Active and adopted from this exact deploy tx.
	if current != nil && current.Status == models.ProjectStatusActive && strings.EqualFold(current.DeployTxHash, match.TxHash) {
		return nil
	}

	_, err = s.VerifyDeployment(ctx, common.HexToHash(match.TxHash), profile)
	return err
}

// latestAuthorizedDeploy picks the newest event in matches (which are already
// filtered to this profile's projectId+profileDigest, ascending) whose deploy
// transaction is bound to the configured admin, or nil when none is.
//
// An unauthorized event is skipped, not recorded: it must never be able to
// touch the stored project, because marking it Failed would overwrite — and so
// take down — a legitimately Active deployment, handing an attacker a denial of
// service in place of the takeover. It is logged once per transaction so an
// operator sees the attempt without the reconcile ticker flooding the log.
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
			log.Printf("project: ignoring ProjectDeployed in tx %s — it matches this profile but is not authorized by the configured admin: %s", e.TxHash, reason)
		}
	}
	return nil, nil
}

// deployAuthorizationError reports why the deploy transaction at txHash is not
// an adoption candidate, or "" when it is one. A non-nil error is an RPC
// failure — "we could not tell", which is not the same as "not authorized" and
// must not be treated as a verdict.
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

// adminBindingError is the deployment-authorization trust anchor: it reports why tx/cfg is not a
// deployment this operator authorized, or "" when it is.
//
// Two independent bindings, both required. The transaction's recovered SENDER
// must be the configured admin — that is a signature over the deploy calldata,
// the one thing an attacker cannot forge — and the ProjectConfig's own admin
// field must name that same address, so a deployment cannot hand its roles to
// someone else even if the admin's wallet were tricked into broadcasting it.
//
// An unset admin address (development, where ADMIN_ADDRESS is optional)
// disables the binding and restores the old adopt-any-matching-event behavior,
// with a warning. Production config refuses to start without ADMIN_ADDRESS.
func (s *Service) adminBindingError(tx *types.Transaction, cfg bindings.ProjectConfig) string {
	if s.adminAddress == (common.Address{}) {
		s.warnUnauthenticatedOnce.Do(func() {
			log.Print("project: WARNING — no admin address configured, so deployment adoption is unauthenticated: any wallet that deploys a stack matching this profile's projectId/profileDigest will be adopted. Set ADMIN_ADDRESS.")
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

// demoteOrphaned mirrors ReconcileMinted's orphan-demotion pattern: a project
// whose adopting ProjectDeployed event is no longer canonical is reset to
// Undeployed with its chain-derived fields cleared, so a genuine redeploy can
// be adopted cleanly and no service keeps running against rolled-back
// addresses.
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

// VerifyDeployment adopts the ProjectDeployed transaction at txHash and runs
// the full post-deploy verification pass. The expected
// projectId/profileDigest/decimals are anchored to the stored,
// digest-verified profile (the binding);
// every OTHER expected value (role holders, treasury, auditor, prices,
// redemptionTimeout, quoteToken) is recovered from the observed deploy
// transaction's INPUT calldata — the exact ProjectConfig the admin's wallet
// signed — and cross-checked against on-chain state. ANY failure marks the
// project Failed with a recorded reason; only a genuine RPC/decode error
// returns a non-nil Go error.
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
			continue // the log must be emitted BY the configured factory
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

	// Recover the intended ProjectConfig from the observed deploy transaction's
	// calldata — the ProjectDeployed event carries none of the role/treasury/
	// auditor/price configuration, so the expected verification allowlist comes
	// from the exact bytes the admin's wallet signed.
	tx, _, err := s.client.TransactionByHash(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("project: fetch deploy transaction: %w", err)
	}
	cfg, err := s.factory.UnpackDeployInput(tx.Data())
	if err != nil {
		return s.markVerificationFailed(ctx, p, "deploy transaction calldata could not be decoded to an RWAFactory.deploy(ProjectConfig): "+err.Error())
	}
	// Cross-check the signed calldata against the profile binding: the deployer
	// must have submitted THIS profile's identity/decimals, not some other one
	// the factory happened to echo into the event.
	if cfg.ProjectID != common.Hash(wantProjectID) {
		return s.markVerificationFailed(ctx, p, "deploy calldata projectId disagrees with the stored asset profile")
	}
	if cfg.ProfileDigest != common.Hash(wantProfileDigest) {
		return s.markVerificationFailed(ctx, p, "deploy calldata profileDigest disagrees with the stored asset profile")
	}
	if cfg.Decimals != profile.TokenDecimals {
		return s.markVerificationFailed(ctx, p, fmt.Sprintf("deploy calldata decimals %d disagrees with the stored asset profile's %d", cfg.Decimals, profile.TokenDecimals))
	}
	// The calldata is only a trustworthy allowlist if this operator authorized
	// the transaction that carried it — otherwise every check below just
	// confirms the attacker's stack agrees with the attacker's own config.
	// ReconcileDeployment already filters unauthorized events out before they
	// get here; this repeats the check for any direct caller.
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

	// The event's version is emitted by the very contract under scrutiny, so
	// trust it only if the live factory.version() agrees.
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

	factoryRolesClean, err := verifyNoFactoryRoles(ctx, s.client, s.factoryAddr, p.Addresses)
	if err != nil {
		return nil, fmt.Errorf("project: verify factory role absence: %w", err)
	}
	if !factoryRolesClean {
		reasons = append(reasons, "factory retains DEFAULT_ADMIN_ROLE on a deployed contract")
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

	// Persist the quote token's ERC-20 decimals once, best-effort: a read
	// failure must NOT fail an otherwise-good deployment.
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

// applyConfig copies the signed ProjectConfig's expected role/treasury/
// auditor/price/timeout values onto the project record so the existing
// verifyRoles / verifyImmutableConfig checks compare on-chain state against
// exactly what the admin's wallet submitted.
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

// markVerificationFailed persists p as Failed with reason recorded and returns
// it as a normal, non-error result.
func (s *Service) markVerificationFailed(ctx context.Context, p *models.Project, reason string) (*models.Project, error) {
	p.Status = models.ProjectStatusFailed
	p.VerificationNote = reason
	p.UpdatedAt = time.Now().UTC()
	if err := s.repo.Upsert(ctx, p); err != nil {
		return nil, fmt.Errorf("project: persist verification failure: %w", err)
	}
	return p, nil
}
