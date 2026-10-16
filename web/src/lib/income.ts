// Client-side rules for income distributions: the prepare request, the createDistribution
// arguments from a Prepared snapshot, deadline checks and the Safe batch
import type { Address, Hex } from "viem";
import type { components } from "./api-types";
import { AmountFormatError, toMinimalUnits } from "./format";
import type { ContractCall } from "./safeBatch";
import {
  createDistributionCall,
  erc20ApproveCall,
  type CreateDistributionInput,
} from "./wallet";

type IncomeDistribution = components["schemas"]["IncomeDistribution"];
type PrepareRequest = components["schemas"]["PrepareIncomeDistributionRequest"];

/** IncomeDistributor.MIN_CLAIM_WINDOW */
export const MIN_CLAIM_WINDOW_SECONDS = 7 * 24 * 3600;

// Extra mining slack beyond the contract's minimum, matching the server's prepare guard
const PREPARE_SLACK_SECONDS = 3600;

// Time allowed between review and mining before the contract's minimum window is at risk
const SEND_SLACK_SECONDS = 120;

/** Earliest claim deadline the prepare endpoint accepts at nowSec */
export function minPrepareDeadline(nowSec: number): number {
  return nowSec + MIN_CLAIM_WINDOW_SECONDS + PREPARE_SLACK_SECONDS;
}

/** Unix seconds for a datetime-local value, or null when it is empty or invalid */
export function parseLocalDeadline(value: string): number | null {
  if (!value) return null;
  const ms = new Date(value).getTime();
  return Number.isNaN(ms) ? null : Math.floor(ms / 1000);
}

/** datetime-local text (local time) for Unix seconds, used as an input's default and minimum */
export function toLocalInputValue(seconds: number): string {
  const d = new Date(seconds * 1000);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Validates the prepare form and scales the total by the quote token's decimals */
export function buildPrepareRequest(
  input: {
    recordBlock: string;
    total: string;
    deadlineSeconds: number | null;
  },
  quoteDecimals: number,
  nowSec: number,
): { request: PrepareRequest } | { error: string } {
  const block = input.recordBlock.trim();
  if (
    !/^\d+$/.test(block) ||
    !Number.isSafeInteger(Number(block)) ||
    Number(block) < 1
  ) {
    return { error: "Record block must be a positive whole number." };
  }
  let total: string;
  try {
    total = toMinimalUnits(input.total, quoteDecimals);
  } catch (err) {
    return {
      error:
        err instanceof AmountFormatError
          ? err.message
          : "Enter the total as a whole-quote-token amount.",
    };
  }
  if (BigInt(total) === 0n) {
    return { error: "The total must be greater than zero." };
  }
  if (input.deadlineSeconds === null) {
    return { error: "Choose a claim deadline." };
  }
  if (input.deadlineSeconds < minPrepareDeadline(nowSec)) {
    return {
      error:
        "The claim deadline must be at least 7 days and 1 hour from now to allow mining time beyond the contract's 7-day minimum.",
    };
  }
  return {
    request: {
      recordBlock: Number(block),
      total,
      claimDeadline: input.deadlineSeconds,
    },
  };
}

/** createDistribution arguments of a Prepared distribution, or an error for malformed fields */
export function toCreateInput(
  d: IncomeDistribution,
): { input: CreateDistributionInput } | { error: string } {
  try {
    return {
      input: {
        merkleRoot: d.merkleRoot as Hex,
        total: BigInt(d.total),
        recordBlock: BigInt(d.recordBlock),
        claimDeadline: BigInt(d.claimDeadline),
        snapshotURI: d.snapshotURI,
        snapshotSha256: d.snapshotSha256 as Hex,
      },
    };
  } catch {
    return { error: "The prepared distribution has malformed numeric fields." };
  }
}

/** Null when the deadline still clears the contract's minimum window, else the reason to prepare again */
export function deadlineSendError(
  claimDeadline: number,
  nowSec: number,
): string | null {
  return claimDeadline >= nowSec + MIN_CLAIM_WINDOW_SECONDS + SEND_SLACK_SECONDS
    ? null
    : "The claim deadline is now too close to the contract's 7-day minimum window. Prepare the distribution again with a later deadline.";
}

/** Approve exactly `total`, then create; one Safe batch keeps the two steps atomic */
export function createDistributionBatch(
  quoteToken: Address,
  distributor: Address,
  input: CreateDistributionInput,
): ContractCall[] {
  return [
    erc20ApproveCall(quoteToken, distributor, input.total),
    createDistributionCall(distributor, input),
  ];
}

/** Null when `next` is a valid later deadline, else the reason; the contract only lets deadlines grow */
export function extensionError(
  current: number,
  next: number | null,
  nowSec: number,
): string | null {
  if (next === null) return "Choose a new claim deadline.";
  if (next <= current)
    return "The new deadline must be later than the current one.";
  if (next <= nowSec) return "The new deadline must be in the future.";
  return null;
}

/** Reclaim needs a created, unreclaimed distribution past its deadline, and an unpaused project */
export function canReclaim(
  d: Pick<IncomeDistribution, "status" | "claimDeadline">,
  paused: boolean,
  nowSec: number,
): boolean {
  return (
    !paused &&
    (d.status === "Active" || d.status === "Expired") &&
    nowSec > d.claimDeadline
  );
}
