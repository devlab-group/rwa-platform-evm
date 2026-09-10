package models

import "time"

// Addresses is the deployed project contract set.
type Addresses struct {
	Token            string `json:"token" bson:"token"`
	Compliance       string `json:"compliance" bson:"compliance"`
	SupplyController string `json:"supplyController" bson:"supplyController"`
	Vault            string `json:"vault" bson:"vault"`
	RedemptionEscrow string `json:"redemptionEscrow" bson:"redemptionEscrow"`
	Strategy         string `json:"strategy" bson:"strategy"`
	QuoteToken       string `json:"quoteToken" bson:"quoteToken"`
}

// ProjectStatus tracks deployment lifecycle.
type ProjectStatus string

const (
	ProjectStatusUndeployed ProjectStatus = "Undeployed"
	ProjectStatusDeploying  ProjectStatus = "Deploying"
	ProjectStatusVerifying  ProjectStatus = "Verifying"
	ProjectStatusActive     ProjectStatus = "Active"
	ProjectStatusFailed     ProjectStatus = "Failed"
)

// Project is the single-tenant deployment record (collection: projects).
// Its Mongo document uses a fixed technical _id (see
// internal/dal/mongodb's projectDocID), NOT ProjectID: the "one
// project per deployment" invariant used to be enforced only in
// application code (Service.Deploy's pre-broadcast Get), and Upsert wrote
// to _id==ProjectID, so two concurrent deploys using two different
// ProjectIDs could each land as a SEPARATE document.
type Project struct {
	ProjectID         string    `json:"projectId" bson:"projectId"`
	Version           string    `json:"version" bson:"version"`
	ChainID           int64     `json:"chainId" bson:"chainId"`
	ProfileDigest     string    `json:"profileDigest" bson:"profileDigest"`
	Addresses         Addresses `json:"addresses" bson:"addresses"`
	Paused            bool      `json:"paused" bson:"paused"`
	Auditor           string    `json:"auditor" bson:"auditor"`
	Treasury          string    `json:"treasury" bson:"treasury"`
	RedemptionManager string    `json:"redemptionManager" bson:"redemptionManager"`
	// Admin/ComplianceOperator/Pricer/Treasurer are the configured
	// role-holder candidates from DeployRequest, kept so post-deploy role
	// verification has addresses to check
	// hasRole against — OZ AccessControl (no Enumerable extension in
	// these contracts) offers no way to enumerate holders without already
	// knowing a candidate.
	Admin                        string `json:"admin,omitempty" bson:"admin,omitempty"`
	ComplianceOperator           string `json:"complianceOperator,omitempty" bson:"complianceOperator,omitempty"`
	Pricer                       string `json:"pricer,omitempty" bson:"pricer,omitempty"`
	Treasurer                    string `json:"treasurer,omitempty" bson:"treasurer,omitempty"`
	QuoteToken                   string `json:"quoteToken" bson:"quoteToken"`
	TokenUnit                    string `json:"tokenUnit" bson:"tokenUnit"`
	PurchasePricePerWholeToken   string `json:"purchasePricePerWholeToken" bson:"purchasePricePerWholeToken"`
	RedemptionPricePerWholeToken string `json:"redemptionPricePerWholeToken" bson:"redemptionPricePerWholeToken"`
	RedemptionTimeout            int64  `json:"redemptionTimeout" bson:"redemptionTimeout"`
	TokenDecimals                uint8  `json:"tokenDecimals" bson:"tokenDecimals"`
	// QuoteDecimals is the ERC-20 decimals() of the quote/collateral token
	// (Addresses.QuoteToken), read on-chain ONCE during post-deploy
	// verification and persisted here so the API/UI can scale
	// quote-denominated amounts without a per-request chain read. omitempty:
	// the quote token is an arbitrary external ERC-20 whose decimals() may be
	// unreadable at setup (RPC failure, or a token that doesn't implement it),
	// in which case this stays unset rather than failing the deployment.
	QuoteDecimals         uint8  `json:"quoteDecimals,omitempty" bson:"quoteDecimals,omitempty"`
	FinalityConfirmations uint64 `json:"finalityConfirmations" bson:"finalityConfirmations"`
	BytecodeVerified      bool   `json:"bytecodeVerified" bson:"bytecodeVerified"`
	// Roles maps a role name (DEFAULT_ADMIN_ROLE, PAUSER_ROLE, ...) to its
	// current holder addresses. Populated by post-deploy verification;
	// empty until that runs.
	Roles     map[string][]string `json:"roles,omitempty" bson:"roles,omitempty"`
	Status    ProjectStatus       `json:"status" bson:"status"`
	CreatedAt time.Time           `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time           `json:"updatedAt" bson:"updatedAt"`

	// DeployTxHash is the hash of the observed on-chain RWAFactory.deploy
	// transaction this project record was adopted from. Deployment is now
	// broadcast from the admin's wallet, not the server: the deployment
	// projector (project.ReconcileDeployment) matches the ProjectDeployed event
	// to the stored Asset Profile and records the emitting transaction here, so
	// a re-run can tell "already adopted this exact deploy" from "a new
	// (possibly reorged-in) one" and demote the project if that transaction's
	// event no longer survives.
	DeployTxHash string `json:"deployTxHash,omitempty" bson:"deployTxHash,omitempty"`

	// VerificationNote records why VerifyDeployment marked this project Failed
	// (any mismatch means the deployment is Failed, never Active-with-metadata).
	// Empty when verification succeeded.
	VerificationNote string `json:"verificationNote,omitempty" bson:"verificationNote,omitempty"`

	// Security is the LIVE governance-authority projection maintained by
	// project.ReconcileSecurity from indexed chain events.
	// It is kept SEPARATE from the deploy-config fields above (Auditor,
	// Treasury, RedemptionManager, Admin, ComplianceOperator, Pricer,
	// Treasurer, PurchasePricePerWholeToken, RedemptionPricePerWholeToken,
	// Roles) precisely so those remain the immutable deploy-time snapshot the
	// projector re-folds every governance event on top of — writing live
	// values back over them would make the fold's own baseline drift. nil
	// until the first projection runs (or when governance indexing is not
	// wired), in which case the API falls back to the deploy-config snapshot.
	Security *SecurityState `json:"security,omitempty" bson:"security,omitempty"`
}

// SecurityState is the live, event-sourced view of a deployed project's
// governance authority — the current paused flag, auditor, treasury, prices,
// and role holders as projected from indexed Pausable / AccessControl /
// Vault / SupplyController / Strategy events by project.ReconcileSecurity.
// AsOfBlock/AsOfTime record how current this projection is (the indexer
// checkpoint it was last folded up to), which the API turns into the
// securityStale signal.
// ForcedTransferState is the bounded summary of one ERC-7943 forced transfer,
// carrying enough identity to find the event itself in canonical chain
// history. Amount is a base-10 string in token minimal units.
type ForcedTransferState struct {
	From        string `json:"from" bson:"from"`
	To          string `json:"to" bson:"to"`
	Amount      string `json:"amount" bson:"amount"`
	TxHash      string `json:"txHash" bson:"txHash"`
	BlockNumber uint64 `json:"blockNumber" bson:"blockNumber"`
	LogIndex    uint   `json:"logIndex" bson:"logIndex"`
}

type SecurityState struct {
	Paused             bool   `json:"paused" bson:"paused"`
	Auditor            string `json:"auditor" bson:"auditor"`
	Treasury           string `json:"treasury" bson:"treasury"`
	RedemptionManager  string `json:"redemptionManager,omitempty" bson:"redemptionManager,omitempty"`
	Admin              string `json:"admin,omitempty" bson:"admin,omitempty"`
	ComplianceOperator string `json:"complianceOperator,omitempty" bson:"complianceOperator,omitempty"`
	Pricer             string `json:"pricer,omitempty" bson:"pricer,omitempty"`
	// PendingAdmin is the incoming DEFAULT_ADMIN of an in-progress two-step
	// admin transfer (AccessControlDefaultAdminRules): the newAdmin of the
	// latest DefaultAdminTransferScheduled that has neither been canceled nor
	// yet accepted on every governance contract. Empty when no transfer is
	// pending. See project.ReconcileSecurity for the accept-clears-via-role-move
	// projection.
	PendingAdmin string `json:"pendingAdmin,omitempty" bson:"pendingAdmin,omitempty"`
	Treasurer    string `json:"treasurer,omitempty" bson:"treasurer,omitempty"`
	// Strategy is the pricing strategy the Vault CURRENTLY points at, folded
	// from Vault.StrategyChanged. Vault.setStrategy is admin-callable, so
	// Addresses.Strategy — written once at deploy verification and never
	// rewritten — is only the baseline. This is what the price projection
	// below reads from, and what the indexer's watched-address set is
	// rebuilt from after a swap; empty until the fold has a value.
	Strategy string `json:"strategy,omitempty" bson:"strategy,omitempty"`
	// PurchasePricePerWholeToken/RedemptionPricePerWholeToken are the live
	// strategy prices. NOTE: the /project API response does not currently
	// expose price fields at all (see dto.ProjectResponse) — flagged as a
	// genuinely-missing exposed field rather than silently added to the
	// frozen openapi contract; they are projected here so the DB record is
	// the source of truth regardless.
	PurchasePricePerWholeToken   string              `json:"purchasePricePerWholeToken,omitempty" bson:"purchasePricePerWholeToken,omitempty"`
	RedemptionPricePerWholeToken string              `json:"redemptionPricePerWholeToken,omitempty" bson:"redemptionPricePerWholeToken,omitempty"`
	Roles                        map[string][]string `json:"roles,omitempty" bson:"roles,omitempty"`
	// FrozenBalances is the ERC-7943 enforcement state: checksummed holder
	// address -> frozen amount in token minimal units, as a base-10 string so
	// a uint256 survives JSON without precision loss. BOUNDED by design: only
	// currently-frozen holders appear, and a Frozen(account, 0) drops the
	// entry rather than recording a zero. nil when nothing is frozen.
	FrozenBalances map[string]string `json:"frozenBalances,omitempty" bson:"frozenBalances,omitempty"`
	// LastForcedTransfer summarizes the most recent canonical ForcedTransfer,
	// as an audit hint only. The full history stays in chain_events and the
	// transaction index; this document never accumulates one entry per
	// seizure.
	LastForcedTransfer *ForcedTransferState `json:"lastForcedTransfer,omitempty" bson:"lastForcedTransfer,omitempty"`
	AsOfBlock          uint64               `json:"asOfBlock" bson:"asOfBlock"`
	AsOfTime           time.Time            `json:"asOfTime" bson:"asOfTime"`
}
