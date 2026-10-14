import { sha256, toBytes, type Address, type Hex } from "viem";
import type { ContractCall } from "./safeBatch";
import { erc20ApproveCall, escrowCall } from "./wallet";

/**
 * Encodes a non-empty rejection reason as bytes32, matching internal/redemption EncodeReasonCode.
 * An exact 0x-prefixed 64-hex-character value passes through; other text uses SHA-256.
 */
export function encodeReasonCode(input: string): Hex {
  if (!input) {
    throw new Error("A rejection reason code is required.");
  }
  if (/^0x[0-9a-fA-F]{64}$/.test(input)) {
    return input as Hex;
  }
  return sha256(toBytes(input));
}

/**
 * Builds exact-amount approval, funding and optional claim calls in that order.
 * When executed atomically with claim, a refused payout also rolls back funding.
 */
export function fundBatchCalls(
  quoteToken: Address,
  escrow: Address,
  id: bigint,
  quoteAmount: bigint,
  claim: boolean,
): ContractCall[] {
  return [
    erc20ApproveCall(quoteToken, escrow, quoteAmount),
    escrowCall(escrow, "fundRedemption", id),
    ...(claim ? [escrowCall(escrow, "claimRedemption", id)] : []),
  ];
}
