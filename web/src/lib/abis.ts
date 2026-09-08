// Minimal ABI fragments for the contract writes this console encodes and
// broadcasts from the connected admin wallet, including the actions on the
// Security page (web-only — the contract's onlyRole check is the real
// authorization, see lib/roles.ts). Only the functions actually encoded here
// are declared; full ABIs live in the server's generated bindings.
import type { Abi } from "viem";

/** RWAToken pause/unpause — gated on-chain by PAUSER_ROLE. */
export const pausableAbi = [
  { type: "function", name: "pause", inputs: [], outputs: [], stateMutability: "nonpayable" },
  { type: "function", name: "unpause", inputs: [], outputs: [], stateMutability: "nonpayable" },
] as const satisfies Abi;

/** FixedPriceStrategy price setters — gated on-chain by PRICER_ROLE. Prices are
 * quote-token minimal units per whole RWA token. */
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

/** AccessControl role management + AccessControlDefaultAdminRules admin transfer. */
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

/** RWAToken's ERC-7943 (uRWA) enforcement calls, both gated on-chain by
 * DEFAULT_ADMIN_ROLE. `setFrozenTokens` writes an absolute amount in RWA
 * minimal units, so 0 releases a hold and a second call replaces the first
 * rather than adding to it. `forcedTransfer` seizes tokens from a holder who
 * cannot or will not sign: the recipient must still be compliance-Allowed, and
 * it works while the project is paused. Signatures are pinned in
 * shared/vectors/erc7943-abi.json (see abis.test.ts). */
export const erc7943Abi = [
  {
    type: "function",
    name: "setFrozenTokens",
    inputs: [
      { name: "account", type: "address" },
      { name: "amount", type: "uint256" },
    ],
    outputs: [],
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
    outputs: [],
    stateMutability: "nonpayable",
  },
] as const satisfies Abi;

/** Vault calls made from the connected wallet. `withdrawProceeds`
 * (TREASURER_ROLE) pays out the vault's accumulated quote-token proceeds and
 * needs no approve — the vault holds and transfers those funds. `amount` is in
 * quote-token minimal units. `previewBuy` is the read the sales-quote widget
 * uses: it asks the vault's strategy what a purchase of `tokenAmount` RWA
 * minimal units costs, in quote-token minimal units. */
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

/** RWAFactory.deploy(ProjectConfig) — the permissionless factory call the admin
 * broadcasts from their own wallet to stand up a project (the `admin` field
 * inside the config becomes DEFAULT_ADMIN_ROLE; there is no on-chain role gate
 * on deploy itself). Field order mirrors IRWAFactory.ProjectConfig exactly; the
 * server observes the resulting ProjectDeployed event. The Deployment return
 * tuple is declared so viem can decode it, though the UI only needs the tx. */
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

/** SupplyController.mint(MintAttestation, signature) — the admin broadcasts the
 * auditor-signed mint from their own wallet. Field order mirrors
 * ISupplyController.MintAttestation exactly; the auditor's EIP-712 signature is
 * verified on-chain (the server no longer relays; it observes the Minted event).
 * No on-chain role gate — a valid auditor signature over an unused nonce is the
 * authorization. */
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

/** RedemptionEscrow writes broadcast from the connected admin wallet:
 * fundRedemption (TREASURER_ROLE — pulls quoteAmount via safeTransferFrom, so
 * the treasurer must approve the escrow first) and rejectRedemption
 * (REDEMPTION_MANAGER_ROLE). */
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
] as const satisfies Abi;
