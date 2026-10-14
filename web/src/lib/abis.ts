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
 * Admin-only ERC-7943 enforcement: setFrozenTokens replaces the absolute RWA amount;
 * forcedTransfer bypasses sender eligibility and pause while checking the recipient.
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
 * Known token and access-control errors for describeWalletError; these fragments encode no calls.
 * Shared ERC-7943 vectors pin selectors, and unlisted errors fall back to wallet messages.
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
 * Vault preview and treasury withdrawal calls.
 * previewBuy returns quote-token minimal units; withdrawProceeds uses those units,
 * pays configured treasury and requires TREASURER_ROLE without an approval.
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
] as const satisfies Abi;

/**
 * Permissionless factory deployment; config.admin receives admin authority.
 * Field order follows IRWAFactory.ProjectConfig, with a Deployment result tuple.
 * The console uses the transaction hash and the server observes ProjectDeployed.
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
 * Wallet-submitted mint with fields matching ISupplyController.MintAttestation.
 * The contract checks the auditor signature and unused nonce; the server observes Minted.
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
] as const satisfies Abi;

/**
 * Redemption funding and rejection calls plus permissionless claim.
 * Funding requires TREASURER_ROLE and quote allowance; rejection requires REDEMPTION_MANAGER_ROLE.
 * A Safe batch can claim immediately after funding.
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
