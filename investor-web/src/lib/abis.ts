// Pinned ABIs for locally encoded transactions and on-chain previews.
// Verify server-supplied project addresses in the transaction preview.
import type { Abi } from "viem";

/**
 * Vault.buy pulls quote tokens after checking maxQuoteAmount; approve Vault first.
 * Caller and recipient must be Allowed. tokenAmount uses RWA minimal units; quotes use
 * quote-token minimal units. previewBuy supplies the price used for the slippage ceiling.
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
