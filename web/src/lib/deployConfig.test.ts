import { describe, expect, it } from "vitest";
import { decodeFunctionData, zeroAddress } from "viem";
import { rwaFactoryAbi, rwaFactoryV3Abi, rwaFactoryV4Abi } from "./abis";
import {
  buildProjectConfigV3,
  buildProjectConfigV4,
  dealingCutoffProblem,
  parseFactoryVersion,
  type DeployValuesV3,
  type DeployValuesV4,
} from "./deployConfig";
import { deployCalldata, type ProjectConfigInput } from "./wallet";
import {
  V3_ORACLE_DEPLOY_CALLDATA,
  V4_ORACLE_DEPLOY_CALLDATA,
} from "../test/deployGoldens";

// Digits only, so the checksummed form viem decodes equals the input
const A = (n: number) => `0x${String(n).padStart(40, "0")}` as const;
const CTX = {
  decimals: 18,
  profileDigest: `0x${"11".repeat(32)}` as const,
  projectId: `0x${"22".repeat(32)}` as const,
  quoteDecimals: 6,
};

const FIXED: DeployValuesV3 = {
  name: "Acme",
  symbol: "ACME",
  quoteToken: A(2),
  strategyKind: "Fixed",
  purchasePrice: "1.5",
  redemptionPrice: "1",
  minPurchase: "25",
  priceFeed: "",
  priceMaxAge: "",
  purchaseSpreadBps: "0",
  redemptionSpreadBps: "0",
  sequencerUptimeFeed: "",
  sequencerGracePeriod: "",
  redemptionTimeout: 604800,
  admin: A(3),
  auditor: A(4),
  complianceOperator: A(5),
  pricer: A(6),
  treasurer: A(7),
  redemptionManager: A(8),
  incomeManager: A(10),
  treasury: A(9),
  adminTransferDelay: 172800,
};

const ORACLE: DeployValuesV3 = {
  ...FIXED,
  strategyKind: "Oracle",
  purchasePrice: "",
  redemptionPrice: "",
  pricer: "",
  priceFeed: A(1000),
  priceMaxAge: "3600",
  purchaseSpreadBps: "50",
  redemptionSpreadBps: "75",
};

function build(v: DeployValuesV3) {
  return buildProjectConfigV3(v, CTX);
}

describe("parseFactoryVersion", () => {
  it("accepts only the three supported versions", () => {
    expect(parseFactoryVersion("rwa-v2")).toBe("rwa-v2");
    expect(parseFactoryVersion("rwa-v3")).toBe("rwa-v3");
    expect(parseFactoryVersion("rwa-v4")).toBe("rwa-v4");
    expect(parseFactoryVersion("")).toBeNull();
    expect(parseFactoryVersion(undefined)).toBeNull();
    expect(parseFactoryVersion("rwa-v5")).toBeNull();
  });
});

describe("buildProjectConfigV3 Fixed", () => {
  it("scales prices and the minimum by quote decimals and zeroes the oracle fields", () => {
    const r = build(FIXED);
    if (!("config" in r)) throw new Error(r.error);
    expect(r.config).toMatchObject({
      purchasePricePerWholeToken: 1_500_000n,
      redemptionPricePerWholeToken: 1_000_000n,
      minPurchaseQuote: 25_000_000n,
      strategyKind: 0,
      pricer: A(6),
      priceFeed: zeroAddress,
      priceMaxAge: 0n,
      purchaseSpreadBps: 0,
      redemptionSpreadBps: 0,
      sequencerUptimeFeed: zeroAddress,
      sequencerGracePeriod: 0n,
      incomeManager: A(10),
    });
  });

  it("treats an empty minimum as none", () => {
    const r = build({ ...FIXED, minPurchase: "" });
    expect("config" in r && r.config.minPurchaseQuote).toBe(0n);
  });

  it("ignores stray oracle inputs when Fixed is selected", () => {
    const r = build({ ...FIXED, priceFeed: A(1), priceMaxAge: "5" });
    expect("config" in r && r.config.priceFeed).toBe(zeroAddress);
  });

  it.each([
    ["zero pricer", { pricer: "" }, /Pricer/],
    ["zero purchase price", { purchasePrice: "0" }, /above 0/],
    ["zero redemption price", { redemptionPrice: "0.000000" }, /above 0/],
    ["no income manager", { incomeManager: "" }, /Income manager/],
    ["zero-address treasury", { treasury: zeroAddress }, /Treasury/],
    ["over-precise price", { purchasePrice: "1.1234567" }, /valid amount/],
    ["over-precise minimum", { minPurchase: "0.0000001" }, /valid amount/],
  ])("rejects %s", (_n, patch, message) => {
    const r = build({ ...FIXED, ...patch });
    expect("error" in r && r.error).toMatch(message);
  });
});

describe("buildProjectConfigV3 Oracle", () => {
  it("zeroes the pricer and prices and keeps the oracle parameters", () => {
    const r = build({
      ...ORACLE,
      sequencerUptimeFeed: A(2000),
      sequencerGracePeriod: "1800",
    });
    if (!("config" in r)) throw new Error(r.error);
    expect(r.config).toMatchObject({
      strategyKind: 1,
      pricer: zeroAddress,
      purchasePricePerWholeToken: 0n,
      redemptionPricePerWholeToken: 0n,
      priceFeed: A(1000),
      priceMaxAge: 3600n,
      purchaseSpreadBps: 50,
      redemptionSpreadBps: 75,
      sequencerUptimeFeed: A(2000),
      sequencerGracePeriod: 1800n,
    });
  });

  it("drops a typed pricer and prices", () => {
    const r = build({
      ...ORACLE,
      pricer: A(6),
      purchasePrice: "9",
      redemptionPrice: "9",
    });
    if (!("config" in r)) throw new Error(r.error);
    expect(r.config.pricer).toBe(zeroAddress);
    expect(r.config.purchasePricePerWholeToken).toBe(0n);
  });

  it("allows the purchase spread at 10000 and the redemption spread at 9999", () => {
    const r = build({
      ...ORACLE,
      purchaseSpreadBps: "10000",
      redemptionSpreadBps: "9999",
    });
    expect("config" in r).toBe(true);
  });

  it.each([
    ["no feed", { priceFeed: "" }, /Price feed/],
    ["zero max age", { priceMaxAge: "0" }, /Max feed age/],
    ["fractional max age", { priceMaxAge: "1.5" }, /whole number/],
    [
      "purchase spread over 10000",
      { purchaseSpreadBps: "10001" },
      /Purchase spread/,
    ],
    [
      "redemption spread at 10000",
      { redemptionSpreadBps: "10000" },
      /Redemption spread/,
    ],
    [
      "sequencer feed without grace",
      { sequencerUptimeFeed: A(2000) },
      /together/,
    ],
    [
      "grace without sequencer feed",
      { sequencerGracePeriod: "60" },
      /together/,
    ],
    [
      "grace beyond uint64",
      {
        sequencerUptimeFeed: A(2000),
        sequencerGracePeriod: "18446744073709551616",
      },
      /grace period/,
    ],
  ])("rejects %s", (_n, patch, message) => {
    const r = build({ ...ORACLE, ...patch });
    expect("error" in r && r.error).toMatch(message);
  });
});

describe("deployCalldata", () => {
  const V2: ProjectConfigInput = {
    name: "Acme",
    symbol: "ACME",
    decimals: 18,
    profileDigest: CTX.profileDigest,
    projectId: CTX.projectId,
    quoteToken: A(2),
    purchasePricePerWholeToken: 1n,
    redemptionPricePerWholeToken: 2n,
    redemptionTimeout: 604800n,
    admin: A(3),
    auditor: A(4),
    complianceOperator: A(5),
    pricer: A(6),
    treasurer: A(7),
    redemptionManager: A(8),
    treasury: A(9),
    adminTransferDelay: 172800,
  };

  it("encodes an rwa-v2 config with the rwa-v2 ABI, unchanged", () => {
    const data = deployCalldata(V2);
    const { args } = decodeFunctionData({ abi: rwaFactoryAbi, data });
    expect(args[0]).toEqual(V2);
    // The v2 tuple has no v3 fields, so the selector differs from the v3 deploy
    expect(data.slice(0, 10)).not.toBe(
      deployCalldata((build(FIXED) as { config: never }).config).slice(0, 10),
    );
  });

  it("encodes an rwa-v3 config with the rwa-v3 ABI in tuple order", () => {
    const built = build(ORACLE);
    if (!("config" in built)) throw new Error(built.error);
    const { args } = decodeFunctionData({
      abi: rwaFactoryV3Abi,
      data: deployCalldata(built.config),
    });
    expect(args[0]).toEqual(built.config);
  });
});

// The same Oracle config as the cast-encoded goldens; every uint64 differs so a swap shows
const GOLDEN_ORACLE_V3: DeployValuesV3 = {
  ...ORACLE,
  sequencerUptimeFeed: A(2000),
  sequencerGracePeriod: "1800",
};
const GOLDEN_ORACLE_V4: DeployValuesV4 = {
  ...GOLDEN_ORACLE_V3,
  dealingCutoff: "7200",
  purchaseTimeout: 1_209_600,
};

describe("deployCalldata against cast-encoded goldens", () => {
  it("encodes an rwa-v3 config to the exact bytes of the rwa-v3 factory", () => {
    const built = build(GOLDEN_ORACLE_V3);
    if (!("config" in built)) throw new Error(built.error);
    expect(deployCalldata(built.config)).toBe(V3_ORACLE_DEPLOY_CALLDATA);
  });

  it("encodes an rwa-v4 config to the exact bytes of the rwa-v4 factory", () => {
    const built = buildProjectConfigV4(GOLDEN_ORACLE_V4, CTX);
    if (!("config" in built)) throw new Error(built.error);
    expect(deployCalldata(built.config)).toBe(V4_ORACLE_DEPLOY_CALLDATA);
  });

  it("does not let an rwa-v4 config fall through to the rwa-v3 encoding", () => {
    const built = buildProjectConfigV4(GOLDEN_ORACLE_V4, CTX);
    if (!("config" in built)) throw new Error(built.error);
    expect(deployCalldata(built.config).slice(0, 10)).toBe("0x29a5a742");
    expect(
      deployCalldata(
        (build(GOLDEN_ORACLE_V3) as { config: never }).config,
      ).slice(0, 10),
    ).toBe("0xc91ac6b4");
  });

  it("round-trips an rwa-v4 config through the rwa-v4 ABI", () => {
    const built = buildProjectConfigV4(GOLDEN_ORACLE_V4, CTX);
    if (!("config" in built)) throw new Error(built.error);
    const { args } = decodeFunctionData({
      abi: rwaFactoryV4Abi,
      data: deployCalldata(built.config),
    });
    expect(args[0]).toEqual(built.config);
  });
});

const DAY = 86_400;
const YEAR = 365 * DAY;

function buildV4(patch: Partial<DeployValuesV4>) {
  return buildProjectConfigV4({ ...GOLDEN_ORACLE_V4, ...patch }, CTX);
}

describe("buildProjectConfigV4 dealing cut-off", () => {
  // Both timeouts are 7 days (604800) in these rows, so the ceiling is 604799
  it.each([
    ["1 second", "1", true],
    ["one below both timeouts", "604799", true],
    ["equal to both timeouts", "604800", false],
    ["above both timeouts", "604801", false],
    ["zero", "0", false],
    ["empty", "", false],
    ["fractional", "1.5", false],
    ["negative", "-5", false],
  ])("cut-off %s", (_name, cutoff, ok) => {
    const r = buildV4({
      dealingCutoff: cutoff,
      redemptionTimeout: 604_800,
      purchaseTimeout: 604_800,
    });
    expect("config" in r).toBe(ok);
  });

  it("rejects a cut-off that reaches only the purchase timeout", () => {
    const r = buildV4({
      dealingCutoff: "200000",
      redemptionTimeout: 604_800,
      purchaseTimeout: 200_000,
    });
    expect("error" in r && r.error).toMatch(/below both/);
  });

  it("rejects a cut-off that reaches only the redemption timeout", () => {
    const r = buildV4({
      dealingCutoff: "200000",
      redemptionTimeout: 200_000,
      purchaseTimeout: 604_800,
    });
    expect("error" in r && r.error).toMatch(/below both/);
  });

  it("sends a zero cut-off for a Fixed project, ignoring a typed one", () => {
    const r = buildV4({
      ...FIXED,
      dealingCutoff: "999",
      purchaseTimeout: 604_800,
    });
    if (!("config" in r)) throw new Error(r.error);
    expect(r.config.dealingCutoff).toBe(0n);
  });

  it("does not require a cut-off for a Fixed project", () => {
    const r = buildV4({
      ...FIXED,
      dealingCutoff: "",
      purchaseTimeout: 604_800,
    });
    expect("config" in r).toBe(true);
  });
});

describe("buildProjectConfigV4 timeouts", () => {
  it.each([
    ["1 day", DAY, true],
    ["one second under 1 day", DAY - 1, false],
    ["365 days", YEAR, true],
    ["one second over 365 days", YEAR + 1, false],
    ["fractional seconds", 90_000.5, false],
    ["zero", 0, false],
  ])("purchase timeout %s", (_name, seconds, ok) => {
    // A cut-off of 3600 stays below every timeout in range
    const r = buildV4({ purchaseTimeout: seconds, dealingCutoff: "3600" });
    expect("config" in r).toBe(ok);
  });

  it.each([
    ["1 day", DAY, true],
    ["one second under 1 day", DAY - 1, false],
    ["365 days", YEAR, true],
    ["one second over 365 days", YEAR + 1, false],
    ["fractional seconds", 90_000.5, false],
    ["not a number", Number.NaN, false],
  ])("redemption timeout %s", (_name, seconds, ok) => {
    const r = buildV4({ redemptionTimeout: seconds, dealingCutoff: "3600" });
    expect("config" in r).toBe(ok);
  });

  it("names a fractional redemption timeout instead of throwing from BigInt", () => {
    expect(
      buildV4({ redemptionTimeout: 90_000.5, dealingCutoff: "3600" }),
    ).toEqual({
      error: "Redemption timeout must be a whole number of seconds.",
    });
  });

  it("names a fractional redemption timeout on the rwa-v3 builder too", () => {
    expect(build({ ...ORACLE, redemptionTimeout: 604_800.25 })).toEqual({
      error: "Redemption timeout must be a whole number of seconds.",
    });
  });

  it("applies the purchase timeout bound to a Fixed project too", () => {
    const r = buildV4({ ...FIXED, purchaseTimeout: DAY - 1 });
    expect("error" in r && r.error).toMatch(/Purchase timeout/);
  });

  it("carries both timeouts into the config as given", () => {
    const r = buildV4({ redemptionTimeout: 300_000, purchaseTimeout: 400_000 });
    if (!("config" in r)) throw new Error(r.error);
    expect(r.config).toMatchObject({
      redemptionTimeout: 300_000n,
      purchaseTimeout: 400_000n,
    });
  });
});

describe("buildProjectConfigV4 keeps the rwa-v3 checks", () => {
  it.each([
    ["no income manager", { incomeManager: "" }, /Income manager/],
    ["no price feed", { priceFeed: "" }, /Price feed/],
    ["purchase spread over 10000", { purchaseSpreadBps: "10001" }, /spread/],
  ])("rejects %s", (_name, patch, message) => {
    const r = buildV4(patch);
    expect("error" in r && r.error).toMatch(message);
  });
});

describe("dealingCutoffProblem", () => {
  it("accepts a cut-off one second below the shorter timeout", () => {
    expect(dealingCutoffProblem(99_999n, 100_000, 200_000)).toBeNull();
    expect(dealingCutoffProblem(99_999n, 200_000, 100_000)).toBeNull();
  });

  it("names the cut-off when it is not above zero", () => {
    expect(dealingCutoffProblem(0n, 100_000, 200_000)).toMatch(/above 0/);
  });

  it("names both timeouts when the cut-off reaches one", () => {
    expect(dealingCutoffProblem(100_000n, 100_000, 200_000)).toMatch(
      /redemption timeout \(100000 s\).*purchase timeout \(200000 s\)/,
    );
  });
});
