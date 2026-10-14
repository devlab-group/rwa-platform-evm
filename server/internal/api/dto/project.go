package dto

import (
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
)

// BootstrapConfigResponse mirrors components.schemas.BootstrapConfig: the chain
// id, the factory address the admin's web wallet broadcasts RWAFactory.deploy
// to, and the operator-configured projectId for the Asset Profile. It is public
// and exposes only these bootstrap parameters.
type BootstrapConfigResponse struct {
	ChainID        int64  `json:"chainId"`
	FactoryAddress string `json:"factoryAddress"`
	// ProjectID is the configured deployment projectId (UUID), or "" when none is
	// set. The admin console refuses to create a profile while it is empty.
	ProjectID string `json:"projectId"`
}

// NewBootstrapConfigResponse builds the bootstrap view from server
// configuration; no model backs it
func NewBootstrapConfigResponse(chainID int64, factoryAddress, projectID string) BootstrapConfigResponse {
	return BootstrapConfigResponse{ChainID: chainID, FactoryAddress: factoryAddress, ProjectID: projectID}
}

// ProjectResponse mirrors components.schemas.Project
type ProjectResponse struct {
	ProjectID     string `json:"projectId"`
	Version       string `json:"version"`
	ChainID       int64  `json:"chainId"`
	Decimals      uint8  `json:"decimals"`
	QuoteDecimals uint8  `json:"quoteDecimals,omitempty"`
	TokenUnit     string `json:"tokenUnit"`
	ProfileDigest string `json:"profileDigest"`
	// Legal comes from the stored Asset Profile and is omitted when absent
	Legal *models.LegalDescriptor `json:"legal,omitempty"`
	// Status is the deployment lifecycle state (Undeployed, Deploying, Verifying,
	// Active or Failed). VerificationNote carries the reason a deployment Failed
	// or a non-fatal verification gap on an Active project (see
	// models.Project.VerificationNote).
	Status            string    `json:"status,omitempty"`
	VerificationNote  string    `json:"verificationNote,omitempty"`
	Addresses         Addresses `json:"addresses"`
	Paused            bool      `json:"paused"`
	Auditor           string    `json:"auditor"`
	Treasury          string    `json:"treasury"`
	RedemptionManager string    `json:"redemptionManager"`
	// PendingAdmin is the incoming DEFAULT_ADMIN of an in-progress two-step admin
	// transfer, taken from Project.Security; empty when none is pending
	PendingAdmin          string      `json:"pendingAdmin,omitempty"`
	FinalityConfirmations uint64      `json:"finalityConfirmations"`
	BytecodeVerified      bool        `json:"bytecodeVerified"`
	Roles                 RoleHolders `json:"roles,omitempty"`

	// PurchasePricePerWholeToken and RedemptionPricePerWholeToken are prices in
	// quote-token minimal units (scale by quoteDecimals for display). Like the
	// governance fields, they come from the projection in Project.Security and
	// fall back to the deploy-time values when the projection has none.
	PurchasePricePerWholeToken   string `json:"purchasePricePerWholeToken,omitempty"`
	RedemptionPricePerWholeToken string `json:"redemptionPricePerWholeToken,omitempty"`

	// Paused, Auditor, Treasury, RedemptionManager, PendingAdmin, Roles and the
	// prices above come from the projection in models.Project.Security, which
	// project.ReconcileSecurity builds from the indexed AccessControl and
	// governance events, so out-of-band changes appear here.
	// SecurityAsOfBlock and SecurityAsOfTime report how current that projection
	// is. SecurityStale is computed from the projection's age and the indexer
	// checkpoint (see securityStale in package api). Before the first projection,
	// the response uses the deploy-time values and reports stale.
	// ERC-7943 enforcement state (frozen wallets, last forced transfer) is not
	// here because the response is public; see EnforcementResponse, and an
	// investor reads their own frozen amount from GET /me/wallet-status.
	SecurityAsOfBlock uint64 `json:"securityAsOfBlock,omitempty"`
	SecurityAsOfTime  string `json:"securityAsOfTime,omitempty"`
	SecurityStale     bool   `json:"securityStale"`
}

// EnforcementResponse mirrors components.schemas.Enforcement: the ERC-7943
// state an operator acts on. It is admin-only because it names the wallets the
// issuer has frozen, which is operational compliance data even though the
// events are public on-chain.
type EnforcementResponse struct {
	// FrozenBalances maps a holder address to the frozen amount held against
	// it, in token minimal units as a base-10 string. Only currently frozen
	// holders appear; omitted entirely when nothing is frozen.
	FrozenBalances map[string]string `json:"frozenBalances,omitempty"`
	// LastForcedTransfer is the most recent forced transfer, as an audit hint; the
	// chain holds the authoritative history
	LastForcedTransfer *ForcedTransfer `json:"lastForcedTransfer,omitempty"`
	// The three fields below mirror those on ProjectResponse and describe the
	// projection these values come from
	SecurityAsOfBlock uint64 `json:"securityAsOfBlock,omitempty"`
	SecurityAsOfTime  string `json:"securityAsOfTime,omitempty"`
	SecurityStale     bool   `json:"securityStale"`
}

// ToEnforcementResponse builds the enforcement view from the security
// projection. A project with no projection reports empty state.
func ToEnforcementResponse(p *models.Project, securityStale bool) EnforcementResponse {
	resp := EnforcementResponse{SecurityStale: securityStale}
	s := p.Security
	if s == nil {
		return resp
	}
	resp.FrozenBalances = s.FrozenBalances
	if f := s.LastForcedTransfer; f != nil {
		resp.LastForcedTransfer = &ForcedTransfer{
			From: f.From, To: f.To, Amount: f.Amount,
			TxHash: f.TxHash, BlockNumber: f.BlockNumber, LogIndex: f.LogIndex,
		}
	}
	resp.SecurityAsOfBlock = s.AsOfBlock
	if !s.AsOfTime.IsZero() {
		resp.SecurityAsOfTime = s.AsOfTime.UTC().Format(time.RFC3339)
	}
	return resp
}

// ForcedTransfer mirrors components.schemas.ForcedTransfer: one ERC-7943
// forced transfer, with the chain coordinates to find it in canonical history
type ForcedTransfer struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Amount      string `json:"amount"`
	TxHash      string `json:"txHash"`
	BlockNumber uint64 `json:"blockNumber"`
	LogIndex    uint   `json:"logIndex"`
}

// Addresses mirrors components.schemas.Addresses
type Addresses struct {
	Token            string `json:"token"`
	Compliance       string `json:"compliance"`
	SupplyController string `json:"supplyController"`
	Vault            string `json:"vault"`
	RedemptionEscrow string `json:"redemptionEscrow"`
	Strategy         string `json:"strategy"`
	QuoteToken       string `json:"quoteToken"`
}

// RoleHolders mirrors components.schemas.RoleHolders
type RoleHolders map[string][]string

// ToProjectResponse maps deploy-time project values to the API view and overlays
// the governance projection (models.Project.Security) when present.
//
// The caller supplies tokenUnit, legal, finalityConfirmations (overriding the stored
// value) and checkpoint-derived securityStale.
func ToProjectResponse(p *models.Project, tokenUnit string, legal *models.LegalDescriptor, finalityConfirmations uint64, securityStale bool) ProjectResponse {
	resp := ProjectResponse{
		ProjectID: p.ProjectID, Version: p.Version, ChainID: p.ChainID, ProfileDigest: p.ProfileDigest,
		Status: string(p.Status), VerificationNote: p.VerificationNote,
		Decimals: p.TokenDecimals, QuoteDecimals: p.QuoteDecimals, TokenUnit: tokenUnit, Legal: legal,
		Addresses: Addresses{
			Token: p.Addresses.Token, Compliance: p.Addresses.Compliance, SupplyController: p.Addresses.SupplyController,
			Vault: p.Addresses.Vault, RedemptionEscrow: p.Addresses.RedemptionEscrow, Strategy: p.Addresses.Strategy,
			QuoteToken: p.Addresses.QuoteToken,
		},
		// Deploy-time values are the fallback until a projection exists
		Paused: p.Paused, Auditor: p.Auditor, Treasury: p.Treasury, RedemptionManager: p.RedemptionManager,
		FinalityConfirmations: finalityConfirmations, BytecodeVerified: p.BytecodeVerified, Roles: RoleHolders(p.Roles),
		PurchasePricePerWholeToken:   p.PurchasePricePerWholeToken,
		RedemptionPricePerWholeToken: p.RedemptionPricePerWholeToken,
		SecurityStale:                securityStale,
	}

	// Overlay the governance projection (project.ReconcileSecurity) when present
	if s := p.Security; s != nil {
		resp.Paused = s.Paused
		// The Vault's strategy can change, and the admin console sends price and
		// PRICER_ROLE transactions to addresses.strategy, so report the current one
		if s.Strategy != "" {
			resp.Addresses.Strategy = s.Strategy
		}
		resp.Auditor = s.Auditor
		resp.Treasury = s.Treasury
		resp.RedemptionManager = s.RedemptionManager
		resp.PendingAdmin = s.PendingAdmin
		resp.Roles = RoleHolders(s.Roles)
		// A price the projection does not carry keeps its deploy-time value
		if s.PurchasePricePerWholeToken != "" {
			resp.PurchasePricePerWholeToken = s.PurchasePricePerWholeToken
		}
		if s.RedemptionPricePerWholeToken != "" {
			resp.RedemptionPricePerWholeToken = s.RedemptionPricePerWholeToken
		}
		resp.SecurityAsOfBlock = s.AsOfBlock
		if !s.AsOfTime.IsZero() {
			resp.SecurityAsOfTime = s.AsOfTime.UTC().Format(time.RFC3339)
		}
	}
	return resp
}
