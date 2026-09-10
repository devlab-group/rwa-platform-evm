// Minimal ABI fragments for the contract calls this app makes: the writes it
// encodes and broadcasts from the connected wallet — all of them investor
// actions taken on the investor's own behalf — plus the two price previews it
// reads. Only the functions actually used here are declared; full ABIs live in
// the server's generated bindings. Pinning the fragments in the client (rather
// than fetching calldata or quotes from the server) is deliberate: a
// compromised server cannot redirect a call it never builds, nor misquote a
// price the browser reads for itself.
import type { Abi } from "viem";

/** Vault.buy — the investor purchase. The vault pulls `maxQuoteAmount`-bounded
 * quote token from the buyer via safeTransferFrom, so the buyer must approve
 * the vault first, and both caller and `recipient` must be Allowed. Amounts are
 * minimal units (`tokenAmount` in RWA decimals, the quote amount in quote-token
 * decimals). previewBuy prices that same `tokenAmount` at the current on-chain
 * price — it is what the slippage ceiling is applied to. */
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

/** RedemptionEscrow writes the investor broadcasts: requestRedemption escrows
 * the RWA (approve the escrow first) and returns the new request id,
 * claimRedemption is permissionless once a request is funded (it always pays
 * the recorded beneficiary), and cancelRedemption refunds the escrowed RWA to
 * the beneficiary after the timeout, plus the previewRedeem view the request
 * form quotes from. Funding and rejection are issuer-side calls and are not
 * part of this app. */
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

/** The RWAToken revert reasons an investor can actually hit, so a failed transfer or
 * redemption reads as a sentence rather than raw hex. Errors only: every call here is
 * encoded from the fragments above, and this is used purely for decoding a revert. */
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
