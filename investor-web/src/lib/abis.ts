// Pinned ABI fragments for wallet transactions and on-chain price previews.
// Encoding and quoting happen locally; project addresses still come from
// server configuration and must be checked in the transaction preview.
import type { Abi } from "viem";

/** Vault.buy is the investor purchase. The vault pulls the quoted amount of
 * quote token from the buyer with safeTransferFrom and reverts if the quote
 * exceeds `maxQuoteAmount`, so the buyer must approve the vault first. Both the
 * caller and `recipient` must be Allowed. Amounts are minimal units
 * (`tokenAmount` in RWA decimals, quote amounts in quote-token decimals).
 * previewBuy prices the same `tokenAmount` at the current on-chain price; the
 * slippage ceiling is applied to that figure. */
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

/** RedemptionEscrow calls the investor makes. requestRedemption escrows the RWA
 * (approve the escrow first) and returns the new request id. claimRedemption is
 * permissionless once a request is funded and always pays the recorded
 * beneficiary. cancelRedemption, callable only by the beneficiary after the
 * timeout, returns the escrowed RWA. previewRedeem is the view the request form
 * quotes from. Funding and rejection are issuer-side calls and are not part of
 * this app. */
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

/** The RWAToken revert reasons an investor can hit, so a failed transfer or
 * redemption reads as a sentence instead of raw hex. Errors only: it is used
 * solely to decode a revert. */
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
