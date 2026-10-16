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
	// IncomeDistributor is the rwa-v3 income contract; empty for rwa-v2 projects
	IncomeDistributor string `json:"incomeDistributor,omitempty" bson:"incomeDistributor,omitempty"`
}

// Strategy kinds of the strategy deployed with a project
const (
	StrategyKindFixed  = "Fixed"
	StrategyKindOracle = "Oracle"
)

// OracleConfig is the OraclePriceStrategy configuration signed in the rwa-v3 deploy
// calldata, which verification compares with the strategy's immutables. Addresses are
// 0x-hex; SequencerUptimeFeed is the zero address and GracePeriod 0 without a sequencer feed.
type OracleConfig struct {
	Feed                string `json:"feed" bson:"feed"`
	MaxAge              uint64 `json:"maxAge" bson:"maxAge"`
	PurchaseSpreadBps   uint16 `json:"purchaseSpreadBps" bson:"purchaseSpreadBps"`
	RedemptionSpreadBps uint16 `json:"redemptionSpreadBps" bson:"redemptionSpreadBps"`
	SequencerUptimeFeed string `json:"sequencerUptimeFeed" bson:"sequencerUptimeFeed"`
	GracePeriod         uint64 `json:"gracePeriod" bson:"gracePeriod"`
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

// Project stores the single deployment in projects under MongoDB's fixed projectDocID,
// rather than ProjectID, enforcing one project document
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
	// Admin, ComplianceOperator, Pricer and Treasurer record expected holders from the
	// adopted deployment. Verification compares enumerated AccessControlEnumerable holders
	// against this signed allowlist.
	Admin              string `json:"admin,omitempty" bson:"admin,omitempty"`
	ComplianceOperator string `json:"complianceOperator,omitempty" bson:"complianceOperator,omitempty"`
	Pricer             string `json:"pricer,omitempty" bson:"pricer,omitempty"`
	Treasurer          string `json:"treasurer,omitempty" bson:"treasurer,omitempty"`
	// IncomeManager is the rwa-v3 INCOME_MANAGER_ROLE holder from the deploy calldata
	IncomeManager string `json:"incomeManager,omitempty" bson:"incomeManager,omitempty"`
	// MinPurchaseQuote is the rwa-v3 deploy-time Vault minimum in quote-token minimal units,
	// "0" for none; empty for rwa-v2. Security.MinPurchaseQuote follows later changes.
	MinPurchaseQuote string `json:"minPurchaseQuote,omitempty" bson:"minPurchaseQuote,omitempty"`
	// StrategyKind is the kind of the deploy-time strategy at Addresses.Strategy, Fixed for
	// rwa-v2; Oracle carries the signed OracleConfig in Oracle
	StrategyKind                 string        `json:"strategyKind,omitempty" bson:"strategyKind,omitempty"`
	Oracle                       *OracleConfig `json:"oracle,omitempty" bson:"oracle,omitempty"`
	QuoteToken                   string        `json:"quoteToken" bson:"quoteToken"`
	TokenUnit                    string        `json:"tokenUnit" bson:"tokenUnit"`
	PurchasePricePerWholeToken   string        `json:"purchasePricePerWholeToken" bson:"purchasePricePerWholeToken"`
	RedemptionPricePerWholeToken string        `json:"redemptionPricePerWholeToken" bson:"redemptionPricePerWholeToken"`
	RedemptionTimeout            int64         `json:"redemptionTimeout" bson:"redemptionTimeout"`
	TokenDecimals                uint8         `json:"tokenDecimals" bson:"tokenDecimals"`
	// QuoteDecimals stores Addresses.QuoteToken's decimals() from deployment verification
	// for API/UI scaling without per-request reads. An unreadable value stays unset and
	// does not fail deployment.
	QuoteDecimals         uint8  `json:"quoteDecimals,omitempty" bson:"quoteDecimals,omitempty"`
	FinalityConfirmations uint64 `json:"finalityConfirmations" bson:"finalityConfirmations"`
	BytecodeVerified      bool   `json:"bytecodeVerified" bson:"bytecodeVerified"`
	// Roles maps names such as DEFAULT_ADMIN_ROLE to holders discovered during deployment
	// verification; it is empty beforehand
	Roles     map[string][]string `json:"roles,omitempty" bson:"roles,omitempty"`
	Status    ProjectStatus       `json:"status" bson:"status"`
	CreatedAt time.Time           `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time           `json:"updatedAt" bson:"updatedAt"`

	// DeployTxHash identifies the wallet-broadcast deployment adopted by
	// project.Service.ReconcileDeployment after matching ProjectDeployed to the stored
	// profile. It distinguishes later deployments and enables demotion if the adopted
	// event is reorged out.
	DeployTxHash string `json:"deployTxHash,omitempty" bson:"deployTxHash,omitempty"`

	// VerificationNote explains failed verification or reorg demotion; successful
	// verification clears it
	VerificationNote string `json:"verificationNote,omitempty" bson:"verificationNote,omitempty"`

	// Security is project.ReconcileSecurity's live governance projection. Deploy-time
	// Auditor, Treasury, RedemptionManager, Admin, ComplianceOperator, Pricer, Treasurer,
	// prices and Roles remain the replay baseline. Until projection runs, or without
	// governance indexing, Security is nil and the API falls back to those values.
	Security *SecurityState `json:"security,omitempty" bson:"security,omitempty"`
}

// ForcedTransferState summarizes one ERC-7943 seizure with canonical event identity.
// Amount is a decimal string in token minimal units.
type ForcedTransferState struct {
	From        string `json:"from" bson:"from"`
	To          string `json:"to" bson:"to"`
	Amount      string `json:"amount" bson:"amount"`
	TxHash      string `json:"txHash" bson:"txHash"`
	BlockNumber uint64 `json:"blockNumber" bson:"blockNumber"`
	LogIndex    uint   `json:"logIndex" bson:"logIndex"`
}

// SecurityState projects pause, auditor, treasury, prices and roles from Pausable,
// AccessControl, Vault, SupplyController and Strategy events. project.ReconcileSecurity
// records checkpoint progress in AsOfBlock/AsOfTime for the API's securityStale calculation.
type SecurityState struct {
	Paused             bool   `json:"paused" bson:"paused"`
	Auditor            string `json:"auditor" bson:"auditor"`
	Treasury           string `json:"treasury" bson:"treasury"`
	RedemptionManager  string `json:"redemptionManager,omitempty" bson:"redemptionManager,omitempty"`
	Admin              string `json:"admin,omitempty" bson:"admin,omitempty"`
	ComplianceOperator string `json:"complianceOperator,omitempty" bson:"complianceOperator,omitempty"`
	Pricer             string `json:"pricer,omitempty" bson:"pricer,omitempty"`
	// PendingAdmin is a scheduled DEFAULT_ADMIN transfer awaiting completion across
	// governance contracts. Empty means none remains pending; project.ReconcileSecurity
	// detects acceptance.
	PendingAdmin string `json:"pendingAdmin,omitempty" bson:"pendingAdmin,omitempty"`
	Treasurer    string `json:"treasurer,omitempty" bson:"treasurer,omitempty"`
	// Strategy follows Vault.StrategyChanged rather than deploy-time Addresses.Strategy.
	// The price projection uses it, and the indexer rebuilds watched addresses after
	// rotation. Empty means no folded value yet.
	Strategy string `json:"strategy,omitempty" bson:"strategy,omitempty"`
	// MinPurchaseQuote follows Vault.MinPurchaseQuoteChanged from the deploy-time minimum;
	// empty for rwa-v2
	MinPurchaseQuote string `json:"minPurchaseQuote,omitempty" bson:"minPurchaseQuote,omitempty"`
	// PurchasePricePerWholeToken and RedemptionPricePerWholeToken are the live
	// strategy prices
	PurchasePricePerWholeToken   string              `json:"purchasePricePerWholeToken,omitempty" bson:"purchasePricePerWholeToken,omitempty"`
	RedemptionPricePerWholeToken string              `json:"redemptionPricePerWholeToken,omitempty" bson:"redemptionPricePerWholeToken,omitempty"`
	Roles                        map[string][]string `json:"roles,omitempty" bson:"roles,omitempty"`
	// RolesByContract maps contract labels to roles and sorted, checksummed holders; Roles is their union
	RolesByContract map[string]map[string][]string `json:"rolesByContract,omitempty" bson:"rolesByContract,omitempty"`
	// FrozenBalances maps checksummed holders to decimal-string frozen amounts in token
	// minimal units, preserving uint256 precision. Frozen amount 0 removes a holder; an
	// empty map is nil.
	FrozenBalances map[string]string `json:"frozenBalances,omitempty" bson:"frozenBalances,omitempty"`
	// LastForcedTransfer is a bounded auditor hint for the latest canonical seizure. Full
	// history remains in chain_events and the transaction index.
	LastForcedTransfer *ForcedTransferState `json:"lastForcedTransfer,omitempty" bson:"lastForcedTransfer,omitempty"`
	AsOfBlock          uint64               `json:"asOfBlock" bson:"asOfBlock"`
	AsOfTime           time.Time            `json:"asOfTime" bson:"asOfTime"`
}
