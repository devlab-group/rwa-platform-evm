// ABI fragments for console-encoded wallet writes and Safe batches.
// Contracts enforce authorization; interfaces live in contracts/src/interfaces.
import type { Abi } from "viem";

/** RWAToken pause controls, requiring PAUSER_ROLE */
export const pausableAbi = [
  {
    type: "function",
    name: "pause",
    inputs: [],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "unpause",
    inputs: [],
    outputs: [],
    stateMutability: "nonpayable",
  },
] as const satisfies Abi;

/** PRICER_ROLE price setters, using quote-token minimal units per whole RWA token */
export const fixedPriceStrategyAbi = [
  {
    type: "function",
    name: "setPurchasePrice",
    inputs: [{ name: "newPrice", type: "uint256" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "setRedemptionPrice",
    inputs: [{ name: "newPrice", type: "uint256" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
] as const satisfies Abi;

/** Role management and delayed admin transfer */
export const accessControlAdminAbi = [
  {
    type: "function",
    name: "grantRole",
    inputs: [
      { name: "role", type: "bytes32" },
      { name: "account", type: "address" },
    ],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "revokeRole",
    inputs: [
      { name: "role", type: "bytes32" },
      { name: "account", type: "address" },
    ],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "beginDefaultAdminTransfer",
    inputs: [{ name: "newAdmin", type: "address" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "acceptDefaultAdminTransfer",
    inputs: [],
    outputs: [],
    stateMutability: "nonpayable",
  },
] as const satisfies Abi;

/**
 * Admin ERC-7943 calls: replace absolute frozen amounts or force transfers past sender
 * eligibility, freeze and pause checks; recipient eligibility still applies.
 * Selectors are pinned in shared/vectors/erc7943-abi.json.
 */
export const erc7943Abi = [
  {
    type: "function",
    name: "setFrozenTokens",
    inputs: [
      { name: "account", type: "address" },
      { name: "amount", type: "uint256" },
    ],
    outputs: [{ name: "result", type: "bool" }],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "forcedTransfer",
    inputs: [
      { name: "from", type: "address" },
      { name: "to", type: "address" },
      { name: "amount", type: "uint256" },
    ],
    outputs: [{ name: "result", type: "bool" }],
    stateMutability: "nonpayable",
  },
] as const satisfies Abi;

/**
 * Error-only fragments for describeWalletError; shared vectors pin ERC-7943 selectors,
 * and unlisted errors fall back to wallet messages
 */
export const rwaTokenErrorsAbi = [
  {
    type: "error",
    name: "ERC7943CannotSend",
    inputs: [{ name: "account", type: "address" }],
  },
  {
    type: "error",
    name: "ERC7943CannotReceive",
    inputs: [{ name: "account", type: "address" }],
  },
  {
    type: "error",
    name: "ERC7943CannotTransfer",
    inputs: [
      { name: "from", type: "address" },
      { name: "to", type: "address" },
      { name: "amount", type: "uint256" },
    ],
  },
  {
    type: "error",
    name: "ERC7943InsufficientUnfrozenBalance",
    inputs: [
      { name: "account", type: "address" },
      { name: "amount", type: "uint256" },
      { name: "unfrozen", type: "uint256" },
    ],
  },
  {
    type: "error",
    name: "SenderNotAllowed",
    inputs: [{ name: "from", type: "address" }],
  },
  {
    type: "error",
    name: "RecipientNotAllowed",
    inputs: [{ name: "to", type: "address" }],
  },
  { type: "error", name: "ZeroAddress", inputs: [] },
  {
    type: "error",
    name: "ForcedTransferToSelf",
    inputs: [{ name: "account", type: "address" }],
  },
  {
    type: "error",
    name: "SystemAddressCannotBeFrozen",
    inputs: [{ name: "account", type: "address" }],
  },
  {
    type: "error",
    name: "SystemAddressCannotBeSeized",
    inputs: [{ name: "account", type: "address" }],
  },
  {
    type: "error",
    name: "AccessControlUnauthorizedAccount",
    inputs: [
      { name: "account", type: "address" },
      { name: "neededRole", type: "bytes32" },
    ],
  },
] as const satisfies Abi;

/**
 * Vault preview/withdrawal use quote-token minimal units; withdrawal pays configured
 * treasury, requires TREASURER_ROLE and needs no approval
 */
export const vaultAbi = [
  {
    type: "function",
    name: "withdrawProceeds",
    inputs: [{ name: "amount", type: "uint256" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "previewBuy",
    inputs: [{ name: "tokenAmount", type: "uint256" }],
    outputs: [{ name: "quoteAmount", type: "uint256" }],
    stateMutability: "view",
  },
  // rwa-v3 only; DEFAULT_ADMIN_ROLE, reverts while paused
  {
    type: "function",
    name: "setMinPurchaseQuote",
    inputs: [{ name: "newMin", type: "uint256" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
] as const satisfies Abi;

/** Vault revert raised by buy when the quote is under the rwa-v3 minimum */
export const vaultErrorsAbi = [
  {
    type: "error",
    name: "QuoteBelowMinimum",
    inputs: [
      { name: "quoted", type: "uint256" },
      { name: "minimum", type: "uint256" },
    ],
  },
] as const satisfies Abi;

/** OraclePriceStrategy read failures; the strategy has no roles and no setters */
export const oracleStrategyErrorsAbi = [
  {
    type: "error",
    name: "StalePrice",
    inputs: [
      { name: "updatedAt", type: "uint256" },
      { name: "maxAge", type: "uint256" },
    ],
  },
  {
    type: "error",
    name: "InvalidPrice",
    inputs: [
      { name: "answer", type: "int256" },
      { name: "updatedAt", type: "uint256" },
    ],
  },
  { type: "error", name: "SequencerDown", inputs: [] },
  {
    type: "error",
    name: "GracePeriodNotOver",
    inputs: [
      { name: "sinceRestart", type: "uint256" },
      { name: "gracePeriod", type: "uint256" },
    ],
  },
  {
    type: "error",
    name: "FeedDecimalsChanged",
    inputs: [
      { name: "expected", type: "uint8" },
      { name: "actual", type: "uint8" },
    ],
  },
] as const satisfies Abi;

/**
 * Permissionless deployment; config.admin receives admin authority. Tuples follow
 * IRWAFactory.ProjectConfig/Deployment; the console uses the hash and server observes
 * ProjectDeployed.
 */
export const rwaFactoryAbi = [
  {
    type: "function",
    name: "deploy",
    inputs: [
      {
        name: "config",
        type: "tuple",
        components: [
          { name: "name", type: "string" },
          { name: "symbol", type: "string" },
          { name: "decimals", type: "uint8" },
          { name: "profileDigest", type: "bytes32" },
          { name: "projectId", type: "bytes32" },
          { name: "quoteToken", type: "address" },
          { name: "purchasePricePerWholeToken", type: "uint256" },
          { name: "redemptionPricePerWholeToken", type: "uint256" },
          { name: "redemptionTimeout", type: "uint64" },
          { name: "admin", type: "address" },
          { name: "auditor", type: "address" },
          { name: "complianceOperator", type: "address" },
          { name: "pricer", type: "address" },
          { name: "treasurer", type: "address" },
          { name: "redemptionManager", type: "address" },
          { name: "treasury", type: "address" },
          { name: "adminTransferDelay", type: "uint48" },
        ],
      },
    ],
    outputs: [
      {
        name: "deployment",
        type: "tuple",
        components: [
          { name: "token", type: "address" },
          { name: "compliance", type: "address" },
          { name: "supplyController", type: "address" },
          { name: "vault", type: "address" },
          { name: "redemptionEscrow", type: "address" },
          { name: "strategy", type: "address" },
        ],
      },
    ],
    stateMutability: "nonpayable",
  },
] as const satisfies Abi;

/**
 * rwa-v3 deployment; same permissionless entry point with the extended ProjectConfig/Deployment
 * tuples of IRWAFactory (strategyKind 0 = Fixed, 1 = Oracle)
 */
export const rwaFactoryV3Abi = [
  {
    type: "function",
    name: "deploy",
    inputs: [
      {
        name: "config",
        type: "tuple",
        components: [
          { name: "name", type: "string" },
          { name: "symbol", type: "string" },
          { name: "decimals", type: "uint8" },
          { name: "profileDigest", type: "bytes32" },
          { name: "projectId", type: "bytes32" },
          { name: "quoteToken", type: "address" },
          { name: "purchasePricePerWholeToken", type: "uint256" },
          { name: "redemptionPricePerWholeToken", type: "uint256" },
          { name: "minPurchaseQuote", type: "uint256" },
          { name: "strategyKind", type: "uint8" },
          { name: "priceFeed", type: "address" },
          { name: "priceMaxAge", type: "uint64" },
          { name: "purchaseSpreadBps", type: "uint16" },
          { name: "redemptionSpreadBps", type: "uint16" },
          { name: "sequencerUptimeFeed", type: "address" },
          { name: "sequencerGracePeriod", type: "uint64" },
          { name: "redemptionTimeout", type: "uint64" },
          { name: "admin", type: "address" },
          { name: "auditor", type: "address" },
          { name: "complianceOperator", type: "address" },
          { name: "pricer", type: "address" },
          { name: "treasurer", type: "address" },
          { name: "redemptionManager", type: "address" },
          { name: "incomeManager", type: "address" },
          { name: "treasury", type: "address" },
          { name: "adminTransferDelay", type: "uint48" },
        ],
      },
    ],
    outputs: [
      {
        name: "deployment",
        type: "tuple",
        components: [
          { name: "token", type: "address" },
          { name: "compliance", type: "address" },
          { name: "supplyController", type: "address" },
          { name: "vault", type: "address" },
          { name: "redemptionEscrow", type: "address" },
          { name: "strategy", type: "address" },
          { name: "incomeDistributor", type: "address" },
        ],
      },
    ],
    stateMutability: "nonpayable",
  },
] as const satisfies Abi;

/**
 * IncomeDistributor: INCOME_MANAGER_ROLE creates distributions with approved funding and extends
 * deadlines. Reclaim is permissionless after expiry while unpaused; views detect existing snapshots.
 */
export const incomeDistributorAbi = [
  {
    type: "function",
    name: "createDistribution",
    inputs: [
      { name: "merkleRoot", type: "bytes32" },
      { name: "total", type: "uint256" },
      { name: "recordBlock", type: "uint64" },
      { name: "claimDeadline", type: "uint64" },
      { name: "snapshotURI", type: "string" },
      { name: "snapshotSha256", type: "bytes32" },
    ],
    outputs: [{ name: "id", type: "uint256" }],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "extendClaimDeadline",
    inputs: [
      { name: "id", type: "uint256" },
      { name: "newDeadline", type: "uint64" },
    ],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "reclaimUnclaimed",
    inputs: [{ name: "id", type: "uint256" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "nextId",
    inputs: [],
    outputs: [{ name: "", type: "uint256" }],
    stateMutability: "view",
  },
  {
    type: "function",
    name: "getDistribution",
    inputs: [{ name: "id", type: "uint256" }],
    outputs: [
      {
        name: "",
        type: "tuple",
        components: [
          { name: "merkleRoot", type: "bytes32" },
          { name: "total", type: "uint256" },
          { name: "claimed", type: "uint256" },
          { name: "recordBlock", type: "uint64" },
          { name: "claimDeadline", type: "uint64" },
          { name: "snapshotSha256", type: "bytes32" },
          { name: "reclaimed", type: "bool" },
        ],
      },
    ],
    stateMutability: "view",
  },
] as const satisfies Abi;

/** Error-only fragments for IncomeDistributor reverts shown by describeWalletError */
export const incomeDistributorErrorsAbi = [
  { type: "error", name: "ProjectPaused", inputs: [] },
  { type: "error", name: "ZeroMerkleRoot", inputs: [] },
  { type: "error", name: "ZeroTotal", inputs: [] },
  { type: "error", name: "ZeroSnapshotHash", inputs: [] },
  { type: "error", name: "EmptySnapshotURI", inputs: [] },
  {
    type: "error",
    name: "RecordBlockNotPast",
    inputs: [
      { name: "recordBlock", type: "uint64" },
      { name: "currentBlock", type: "uint256" },
    ],
  },
  {
    type: "error",
    name: "DeadlineNotFuture",
    inputs: [
      { name: "claimDeadline", type: "uint64" },
      { name: "nowTs", type: "uint256" },
    ],
  },
  {
    type: "error",
    name: "ClaimWindowTooShort",
    inputs: [
      { name: "claimDeadline", type: "uint64" },
      { name: "minDeadline", type: "uint256" },
    ],
  },
  {
    type: "error",
    name: "DeadlineNotExtended",
    inputs: [
      { name: "currentDeadline", type: "uint64" },
      { name: "newDeadline", type: "uint64" },
    ],
  },
  {
    type: "error",
    name: "UnknownDistribution",
    inputs: [{ name: "id", type: "uint256" }],
  },
  {
    type: "error",
    name: "ClaimWindowClosed",
    inputs: [
      { name: "claimDeadline", type: "uint64" },
      { name: "nowTs", type: "uint256" },
    ],
  },
  {
    type: "error",
    name: "ClaimWindowOpen",
    inputs: [
      { name: "claimDeadline", type: "uint64" },
      { name: "nowTs", type: "uint256" },
    ],
  },
  {
    type: "error",
    name: "AlreadyReclaimed",
    inputs: [{ name: "id", type: "uint256" }],
  },
  {
    type: "error",
    name: "QuoteDeltaMismatch",
    inputs: [
      { name: "expected", type: "uint256" },
      { name: "actual", type: "uint256" },
    ],
  },
] as const satisfies Abi;

/**
 * Wallet mint follows ISupplyController.MintAttestation; signature/nonce checks precede indexed
 * Minted
 */
export const supplyControllerAbi = [
  {
    type: "function",
    name: "mint",
    inputs: [
      {
        name: "attestation",
        type: "tuple",
        components: [
          { name: "auditor", type: "address" },
          { name: "profileDigest", type: "bytes32" },
          { name: "recordKey", type: "bytes32" },
          { name: "metadataDigest", type: "bytes32" },
          { name: "amount", type: "uint256" },
          { name: "nonce", type: "uint256" },
          { name: "validUntil", type: "uint64" },
          { name: "vault", type: "address" },
        ],
      },
      { name: "signature", type: "bytes" },
    ],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "burn",
    inputs: [
      {
        name: "attestation",
        type: "tuple",
        components: [
          { name: "auditor", type: "address" },
          { name: "profileDigest", type: "bytes32" },
          { name: "operationId", type: "bytes32" },
          { name: "metadataDigest", type: "bytes32" },
          { name: "amount", type: "uint256" },
          { name: "nonce", type: "uint256" },
          { name: "validUntil", type: "uint64" },
          { name: "vault", type: "address" },
        ],
      },
      { name: "signature", type: "bytes" },
    ],
    outputs: [],
    stateMutability: "nonpayable",
  },
] as const satisfies Abi;

/** Mint and burn errors displayed as plain-language messages */
export const supplyControllerErrorsAbi = [
  {
    type: "error",
    name: "AttestationExpired",
    inputs: [
      { name: "validUntil", type: "uint64" },
      { name: "nowTs", type: "uint256" },
    ],
  },
  {
    type: "error",
    name: "InsufficientVaultInventory",
    inputs: [
      { name: "amount", type: "uint256" },
      { name: "balance", type: "uint256" },
    ],
  },
  { type: "error", name: "InvalidSignature", inputs: [] },
  {
    type: "error",
    name: "NonceAlreadyUsed",
    inputs: [{ name: "nonce", type: "uint256" }],
  },
  {
    type: "error",
    name: "OperationIdAlreadyUsed",
    inputs: [{ name: "operationId", type: "bytes32" }],
  },
  { type: "error", name: "ProjectPaused", inputs: [] },
  {
    type: "error",
    name: "RecordKeyAlreadyUsed",
    inputs: [{ name: "recordKey", type: "bytes32" }],
  },
  {
    type: "error",
    name: "WrongAuditor",
    inputs: [
      { name: "expected", type: "address" },
      { name: "provided", type: "address" },
    ],
  },
  {
    type: "error",
    name: "WrongProfileDigest",
    inputs: [
      { name: "expected", type: "bytes32" },
      { name: "provided", type: "bytes32" },
    ],
  },
  {
    type: "error",
    name: "WrongVault",
    inputs: [
      { name: "expected", type: "address" },
      { name: "provided", type: "address" },
    ],
  },
  { type: "error", name: "ZeroAmount", inputs: [] },
] as const satisfies Abi;

/**
 * Funding needs TREASURER_ROLE and quote allowance; rejection needs REDEMPTION_MANAGER_ROLE.
 * Safe batches may append permissionless claim after funding.
 */
export const redemptionEscrowAbi = [
  {
    type: "function",
    name: "fundRedemption",
    inputs: [{ name: "id", type: "uint256" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "rejectRedemption",
    inputs: [
      { name: "id", type: "uint256" },
      { name: "reasonCode", type: "bytes32" },
    ],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "claimRedemption",
    inputs: [{ name: "id", type: "uint256" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
] as const satisfies Abi;
