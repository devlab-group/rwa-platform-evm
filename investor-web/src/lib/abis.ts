// Pinned ABIs for locally encoded transactions and on-chain previews.
// Verify server-supplied project addresses in the transaction preview.
import type { Abi } from "viem";

/**
 * Vault.buy pulls quote tokens after checking maxQuoteAmount; approve Vault first.
 * Caller and recipient must be Allowed. tokenAmount uses RWA minimal units; quotes use
 * quote-token minimal units. previewBuy supplies the price used for the slippage ceiling.
 * On forward-priced projects buy reverts; requestPurchase pulls maxQuoteAmount as a deposit,
 * settlePurchase is permissionless once a round prices the order, and only the buyer can
 * cancelPurchase after the timeout when no usable round exists.
 */
export const vaultAbi = [
  {
    type: "function",
    name: "buy",
    inputs: [
      { name: "tokenAmount", type: "uint256" },
      { name: "maxQuoteAmount", type: "uint256" },
      { name: "recipient", type: "address" },
      { name: "deadline", type: "uint64" },
    ],
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
  {
    type: "function",
    name: "requestPurchase",
    inputs: [
      { name: "tokenAmount", type: "uint256" },
      { name: "maxQuoteAmount", type: "uint256" },
      { name: "recipient", type: "address" },
      { name: "deadline", type: "uint64" },
    ],
    outputs: [{ name: "id", type: "uint256" }],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "settlePurchase",
    inputs: [{ name: "id", type: "uint256" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "cancelPurchase",
    inputs: [{ name: "id", type: "uint256" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
] as const satisfies Abi;

/**
 * Escrow investor calls: requestRedemption pulls approved RWA and assigns a request ID;
 * funded claim is permissionless and pays the recorded beneficiary. Only the beneficiary
 * can cancel a Pending request after timeout. previewRedeem supplies request quotes;
 * funding and rejection are issuer-side calls.
 */
export const redemptionEscrowAbi = [
  {
    type: "function",
    name: "requestRedemption",
    inputs: [
      { name: "rwaAmount", type: "uint256" },
      { name: "minQuoteOut", type: "uint256" },
      { name: "deadline", type: "uint64" },
    ],
    outputs: [{ name: "id", type: "uint256" }],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "claimRedemption",
    inputs: [{ name: "id", type: "uint256" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "cancelRedemption",
    inputs: [{ name: "id", type: "uint256" }],
    outputs: [],
    stateMutability: "nonpayable",
  },
  {
    type: "function",
    name: "previewRedeem",
    inputs: [{ name: "rwaAmount", type: "uint256" }],
    outputs: [{ name: "quoteAmount", type: "uint256" }],
    stateMutability: "view",
  },
] as const satisfies Abi;

/** Token errors used only to decode failed transfers/redemptions into readable messages */
export const tokenErrorsAbi = [
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
] as const satisfies Abi;

/**
 * Vault errors decoded into readable messages: the purchase minimum (rwa-v3) and the
 * forward-order errors (rwa-v4)
 */
export const vaultErrorsAbi = [
  {
    type: "error",
    name: "QuoteBelowMinimum",
    inputs: [
      { name: "quoted", type: "uint256" },
      { name: "minimum", type: "uint256" },
    ],
  },
  { type: "error", name: "ProjectPaused", inputs: [] },
  { type: "error", name: "ForwardPriced", inputs: [] },
  { type: "error", name: "NotForwardPriced", inputs: [] },
  {
    type: "error",
    name: "NotPending",
    inputs: [{ name: "id", type: "uint256" }],
  },
  {
    type: "error",
    name: "NotBuyer",
    inputs: [
      { name: "caller", type: "address" },
      { name: "buyer", type: "address" },
    ],
  },
  {
    type: "error",
    name: "BuyerNotAllowed",
    inputs: [{ name: "account", type: "address" }],
  },
  {
    type: "error",
    name: "TimeoutNotReached",
    inputs: [
      { name: "availableAt", type: "uint64" },
      { name: "nowTs", type: "uint256" },
    ],
  },
  {
    type: "error",
    name: "RoundAvailable",
    inputs: [
      { name: "id", type: "uint256" },
      { name: "roundId", type: "uint80" },
    ],
  },
] as const satisfies Abi;

/** Oracle strategy errors raised by previewBuy, buy, previewRedeem and requestRedemption */
export const oracleErrorsAbi = [
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
      { name: "startedAt", type: "uint256" },
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
 * Read the order's stored strategy for the first round published at least the dealing cutoff
 * after the order; RoundNotAvailable covers missing or unreadable rounds
 */
export const oracleForwardAbi = [
  {
    type: "function",
    name: "quotePurchaseAfter",
    inputs: [
      { name: "tokenAmount", type: "uint256" },
      { name: "baseRound", type: "uint80" },
      { name: "orderTime", type: "uint64" },
    ],
    outputs: [
      { name: "quoteAmount", type: "uint256" },
      { name: "roundId", type: "uint80" },
    ],
    stateMutability: "view",
  },
  {
    type: "error",
    name: "RoundNotAvailable",
    inputs: [{ name: "roundId", type: "uint80" }],
  },
] as const satisfies Abi;

/** IncomeDistributor.claim: a holder claims its own entitlement with a Merkle proof */
export const incomeDistributorAbi = [
  {
    type: "function",
    name: "claim",
    inputs: [
      { name: "id", type: "uint256" },
      { name: "amount", type: "uint256" },
      { name: "proof", type: "bytes32[]" },
    ],
    outputs: [],
    stateMutability: "nonpayable",
  },
  { type: "error", name: "ProjectPaused", inputs: [] },
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
    name: "AlreadyClaimed",
    inputs: [
      { name: "id", type: "uint256" },
      { name: "account", type: "address" },
    ],
  },
  {
    type: "error",
    name: "CallerNotAllowed",
    inputs: [{ name: "account", type: "address" }],
  },
  { type: "error", name: "ZeroAmount", inputs: [] },
  { type: "error", name: "InvalidProof", inputs: [] },
  {
    type: "error",
    name: "ExceedsTotal",
    inputs: [
      { name: "id", type: "uint256" },
      { name: "claimedAfter", type: "uint256" },
      { name: "total", type: "uint256" },
    ],
  },
] as const satisfies Abi;
