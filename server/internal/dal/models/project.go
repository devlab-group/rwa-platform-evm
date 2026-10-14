package models

import "time"

// Addresses is the deployed project contract set
type Addresses struct {
	Token            string `json:"token" bson:"token"`
	Compliance       string `json:"compliance" bson:"compliance"`
	SupplyController string `json:"supplyController" bson:"supplyController"`
	Vault            string `json:"vault" bson:"vault"`
	RedemptionEscrow string `json:"redemptionEscrow" bson:"redemptionEscrow"`
	Strategy         string `json:"strategy" bson:"strategy"`
	QuoteToken       string `json:"quoteToken" bson:"quoteToken"`
}

// ProjectStatus tracks deployment lifecycle
type ProjectStatus string

const (
	ProjectStatusUndeployed ProjectStatus = "Undeployed"
	ProjectStatusDeploying  ProjectStatus = "Deploying"
	ProjectStatusVerifying  ProjectStatus = "Verifying"
	ProjectStatusActive     ProjectStatus = "Active"
	ProjectStatusFailed     ProjectStatus = "Failed"
)

// Project is the single-tenant deployment record (collection: projects). Its
// MongoDB document has a fixed _id (projectDocID in internal/dal/mongodb) and not
// ProjectID, so the database guarantees one project per deployment.
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
	// Admin, ComplianceOperator, Pricer and Treasurer are the role-holder candidates
	// from the adopted deploy transaction. They let post-deploy verification check
	// hasRole, because OpenZeppelin AccessControl without the Enumerable extension
	// cannot list holders unless a candidate is already known.
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
	// QuoteDecimals is the decimals() of the quote token (Addresses.QuoteToken). It is
	// read once during post-deploy verification and stored, so the API and UI can scale
	// quote amounts without a chain read per request. It is omitted when decimals()
	// cannot be read, because the quote token is an arbitrary ERC-20, and the
	// deployment does not fail for that.
	QuoteDecimals         uint8  `json:"quoteDecimals,omitempty" bson:"quoteDecimals,omitempty"`
	FinalityConfirmations uint64 `json:"finalityConfirmations" bson:"finalityConfirmations"`
	BytecodeVerified      bool   `json:"bytecodeVerified" bson:"bytecodeVerified"`
	// Roles maps a role name, such as DEFAULT_ADMIN_ROLE, to its holder addresses. It
	// is filled by post-deploy verification and is empty until then.
	Roles     map[string][]string `json:"roles,omitempty" bson:"roles,omitempty"`
	Status    ProjectStatus       `json:"status" bson:"status"`
	CreatedAt time.Time           `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time           `json:"updatedAt" bson:"updatedAt"`

	// DeployTxHash is the hash of the RWAFactory.deploy transaction this record was
	// adopted from. The admin's wallet broadcasts the deployment, and
	// project.Service.ReconcileDeployment matches the ProjectDeployed event to the
	// stored Asset Profile and records the transaction here. A later run can then tell
	// an already adopted deployment from a new one, and demote the project if the
	// event no longer survives a reorg.
	DeployTxHash string `json:"deployTxHash,omitempty" bson:"deployTxHash,omitempty"`

	// VerificationNote records why verification marked the project Failed, or why it
	// was demoted after a reorg. It is empty when verification succeeded.
	VerificationNote string `json:"verificationNote,omitempty" bson:"verificationNote,omitempty"`

	// Security is the live governance projection kept by project.ReconcileSecurity from
	// indexed chain events. The deploy-time fields above (Auditor, Treasury,
	// RedemptionManager, Admin, ComplianceOperator, Pricer, Treasurer, the prices and
	// Roles) are a baseline that the projector replays every event on top of, so they
	// are not overwritten with live values. Security is nil until the first projection
	// runs or when governance indexing is not wired, and the API then uses the
	// deploy-time values.
	Security *SecurityState `json:"security,omitempty" bson:"security,omitempty"`
}

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

// SecurityState is the live view of a deployed project's governance authority:
// the paused flag, auditor, treasury, prices and role holders, projected from
// indexed Pausable, AccessControl, Vault, SupplyController and Strategy events by
// project.ReconcileSecurity. AsOfBlock and AsOfTime record the indexer checkpoint
// the projection was last built up to, which the API turns into the securityStale
// signal.
type SecurityState struct {
	Paused             bool   `json:"paused" bson:"paused"`
	Auditor            string `json:"auditor" bson:"auditor"`
	Treasury           string `json:"treasury" bson:"treasury"`
	RedemptionManager  string `json:"redemptionManager,omitempty" bson:"redemptionManager,omitempty"`
	Admin              string `json:"admin,omitempty" bson:"admin,omitempty"`
	ComplianceOperator string `json:"complianceOperator,omitempty" bson:"complianceOperator,omitempty"`
	Pricer             string `json:"pricer,omitempty" bson:"pricer,omitempty"`
	// PendingAdmin is the incoming DEFAULT_ADMIN of a two-step admin transfer
	// (AccessControlDefaultAdminRules) that has been scheduled and neither canceled nor
	// accepted on every governance contract. It is empty when no transfer is pending.
	// See project.ReconcileSecurity for how acceptance is detected.
	PendingAdmin string `json:"pendingAdmin,omitempty" bson:"pendingAdmin,omitempty"`
	Treasurer    string `json:"treasurer,omitempty" bson:"treasurer,omitempty"`
	// Strategy is the pricing strategy the Vault currently uses, folded from
	// Vault.StrategyChanged. Vault.setStrategy is callable by the admin, so
	// Addresses.Strategy is only the deploy-time value. The price projection reads
	// this address, and the indexer's watched addresses are rebuilt from it after a
	// change. It is empty until the fold has a value.
	Strategy string `json:"strategy,omitempty" bson:"strategy,omitempty"`
	// PurchasePricePerWholeToken and RedemptionPricePerWholeToken are the live
	// strategy prices
	PurchasePricePerWholeToken   string              `json:"purchasePricePerWholeToken,omitempty" bson:"purchasePricePerWholeToken,omitempty"`
	RedemptionPricePerWholeToken string              `json:"redemptionPricePerWholeToken,omitempty" bson:"redemptionPricePerWholeToken,omitempty"`
	Roles                        map[string][]string `json:"roles,omitempty" bson:"roles,omitempty"`
	// FrozenBalances maps each currently frozen holder (checksummed address) to the
	// frozen amount in token minimal units, as a base-10 string so a uint256 keeps its
	// precision. A Frozen event with amount 0 removes the holder. It is nil when
	// nothing is frozen.
	FrozenBalances map[string]string `json:"frozenBalances,omitempty" bson:"frozenBalances,omitempty"`
	// LastForcedTransfer summarizes the latest canonical ForcedTransfer, as a hint for
	// auditors. The full history stays in chain_events and the transaction index, so
	// this document does not grow with each seizure.
	LastForcedTransfer *ForcedTransferState `json:"lastForcedTransfer,omitempty" bson:"lastForcedTransfer,omitempty"`
	AsOfBlock          uint64               `json:"asOfBlock" bson:"asOfBlock"`
	AsOfTime           time.Time            `json:"asOfTime" bson:"asOfTime"`
}
