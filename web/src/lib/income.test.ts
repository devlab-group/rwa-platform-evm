import { describe, expect, it } from "vitest";
import { decodeFunctionData, erc20Abi } from "viem";
import { incomeDistributorAbi } from "./abis";
import type { components } from "./api-types";
import {
  buildPrepareRequest,
  canReclaim,
  createDistributionBatch,
  deadlineSendError,
  extensionError,
  minPrepareDeadline,
  parseLocalDeadline,
  toCreateInput,
} from "./income";
import { buildSafeBatch } from "./safeBatch";

type Distribution = components["schemas"]["IncomeDistribution"];

const NOW = 1_800_000_000;
const DAY = 86_400;
const MIN = minPrepareDeadline(NOW);
const A = (n: number) => `0x${n.toString(16).padStart(40, "0")}` as const;

const PREPARED: Distribution = {
  preparationId: "prep-1",
  status: "Prepared",
  merkleRoot: `0x${"ab".repeat(32)}`,
  total: "1000000000",
  recordBlock: 1234,
  claimDeadline: MIN,
  snapshotURI: "ipfs://bafy",
  snapshotSha256: `0x${"cd".repeat(32)}`,
  eligibleSupply: "5",
  distributed: "999999999",
  holderCount: 3,
  createdAt: "2026-10-16T00:00:00Z",
};

describe("minPrepareDeadline", () => {
  it("is 7 days plus 1 hour from now", () => {
    expect(MIN - NOW).toBe(7 * DAY + 3600);
  });
});

describe("parseLocalDeadline", () => {
  it("returns Unix seconds for a datetime-local value", () => {
    expect(parseLocalDeadline("2030-01-01T00:00")).toBe(
      Math.floor(new Date("2030-01-01T00:00").getTime() / 1000),
    );
  });
  it("returns null for empty or invalid input", () => {
    expect(parseLocalDeadline("")).toBeNull();
    expect(parseLocalDeadline("garbage")).toBeNull();
  });
});

describe("buildPrepareRequest", () => {
  const ok = { recordBlock: "100", total: "1250.5", deadlineSeconds: MIN };

  it("scales the total by quote decimals and passes block and deadline through", () => {
    expect(buildPrepareRequest(ok, 6, NOW)).toEqual({
      request: { recordBlock: 100, total: "1250500000", claimDeadline: MIN },
    });
  });

  it("accepts a deadline exactly at the minimum and rejects one second earlier", () => {
    expect("request" in buildPrepareRequest(ok, 6, NOW)).toBe(true);
    const early = buildPrepareRequest(
      { ...ok, deadlineSeconds: MIN - 1 },
      6,
      NOW,
    );
    expect("error" in early && early.error).toMatch(/7 days and 1 hour/);
  });

  it("rejects a deadline of exactly 7 days, which would revert once mined", () => {
    const r = buildPrepareRequest(
      { ...ok, deadlineSeconds: NOW + 7 * DAY },
      6,
      NOW,
    );
    expect("error" in r).toBe(true);
  });

  it.each([
    ["empty block", { recordBlock: "" }],
    ["zero block", { recordBlock: "0" }],
    ["fractional block", { recordBlock: "1.5" }],
    ["negative block", { recordBlock: "-3" }],
    ["unsafe block", { recordBlock: "9007199254740993" }],
    ["zero total", { total: "0" }],
    ["over-precise total", { total: "1.0000001" }],
    ["text total", { total: "abc" }],
    ["no deadline", { deadlineSeconds: null }],
  ])("rejects %s", (_n, patch) => {
    expect("error" in buildPrepareRequest({ ...ok, ...patch }, 6, NOW)).toBe(
      true,
    );
  });
});

describe("toCreateInput and createDistributionBatch", () => {
  it("maps a Prepared distribution to createDistribution arguments", () => {
    const r = toCreateInput(PREPARED);
    if (!("input" in r)) throw new Error(r.error);
    expect(r.input).toEqual({
      merkleRoot: PREPARED.merkleRoot,
      total: 1_000_000_000n,
      recordBlock: 1234n,
      claimDeadline: BigInt(MIN),
      snapshotURI: "ipfs://bafy",
      snapshotSha256: PREPARED.snapshotSha256,
    });
  });

  it("reports a malformed total instead of throwing", () => {
    expect("error" in toCreateInput({ ...PREPARED, total: "1e9" })).toBe(true);
  });

  it("builds approve(total) then createDistribution, in that order, for a Safe", () => {
    const r = toCreateInput(PREPARED);
    if (!("input" in r)) throw new Error(r.error);
    const calls = createDistributionBatch(A(0x70), A(0x80), r.input);
    expect(calls).toHaveLength(2);

    expect(calls[0].to).toBe(A(0x70));
    const approve = decodeFunctionData({ abi: erc20Abi, data: calls[0].data });
    expect(approve.functionName).toBe("approve");
    expect(approve.args).toEqual([A(0x80), 1_000_000_000n]);

    expect(calls[1].to).toBe(A(0x80));
    const create = decodeFunctionData({
      abi: incomeDistributorAbi,
      data: calls[1].data,
    });
    expect(create.functionName).toBe("createDistribution");
    expect(create.args).toEqual([
      PREPARED.merkleRoot,
      1_000_000_000n,
      1234n,
      BigInt(MIN),
      "ipfs://bafy",
      PREPARED.snapshotSha256,
    ]);

    const file = buildSafeBatch({
      chainId: 31337,
      safe: A(0x5af),
      name: "Create income distribution",
      description: "d",
      calls,
      createdAt: 1,
    });
    expect(file.transactions.map((t) => [t.to, t.value])).toEqual([
      [A(0x70), "0"],
      [A(0x80), "0"],
    ]);
  });
});

describe("deadlineSendError", () => {
  it("passes a deadline with the 7-day window plus slack and fails one right at 7 days", () => {
    expect(deadlineSendError(NOW + 7 * DAY + 120, NOW)).toBeNull();
    expect(deadlineSendError(NOW + 7 * DAY + 119, NOW)).toMatch(/too close/);
    expect(deadlineSendError(NOW + 7 * DAY, NOW)).toMatch(/too close/);
  });
});

describe("extensionError", () => {
  const current = NOW + 10 * DAY;
  it("accepts only a later, future deadline", () => {
    expect(extensionError(current, current + 1, NOW)).toBeNull();
    expect(extensionError(current, current, NOW)).toMatch(/later/);
    expect(extensionError(current, current - 1, NOW)).toMatch(/later/);
    expect(extensionError(current, null, NOW)).toMatch(/Choose/);
  });
  it("rejects a deadline already in the past when the current one has passed", () => {
    expect(extensionError(NOW - 10 * DAY, NOW - 5 * DAY, NOW)).toMatch(
      /future/,
    );
  });
});

describe("canReclaim", () => {
  const d = { status: "Active", claimDeadline: NOW } as const;
  it("needs an unpaused project and a passed deadline", () => {
    expect(canReclaim(d, false, NOW + 1)).toBe(true);
    expect(canReclaim(d, false, NOW)).toBe(false);
    expect(canReclaim(d, true, NOW + 1)).toBe(false);
  });
  it("never offers reclaim for Prepared or Reclaimed distributions", () => {
    expect(canReclaim({ ...d, status: "Prepared" }, false, NOW + 1)).toBe(
      false,
    );
    expect(canReclaim({ ...d, status: "Reclaimed" }, false, NOW + 1)).toBe(
      false,
    );
    expect(canReclaim({ ...d, status: "Expired" }, false, NOW + 1)).toBe(true);
  });
});
