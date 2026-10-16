import { describe, expect, it } from "vitest";
import { decodeFunctionData, zeroAddress } from "viem";
import { rwaFactoryAbi, rwaFactoryV3Abi } from "./abis";
import {
  buildProjectConfigV3,
  parseFactoryVersion,
  type DeployValuesV3,
} from "./deployConfig";
import { deployCalldata, type ProjectConfigInput } from "./wallet";

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
  it("accepts only the two supported versions", () => {
    expect(parseFactoryVersion("rwa-v2")).toBe("rwa-v2");
    expect(parseFactoryVersion("rwa-v3")).toBe("rwa-v3");
    expect(parseFactoryVersion("")).toBeNull();
    expect(parseFactoryVersion(undefined)).toBeNull();
    expect(parseFactoryVersion("rwa-v4")).toBeNull();
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
