import { sha256, toBytes, type Address, type Hex } from "viem";
import type { ContractCall } from "./safeBatch";
import { erc20ApproveCall, escrowCall } from "./wallet";

/**
 * Passes through exact 0x-prefixed bytes32 hex; hashes other non-empty reasons with SHA-256,
 * matching server EncodeReasonCode
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
 * Orders exact-amount approval, funding and optional claim; atomic execution rolls back funding on
 * payout failure
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
