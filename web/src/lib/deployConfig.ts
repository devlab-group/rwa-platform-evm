// Builds rwa-v3 ProjectConfig from form values, checking per-strategy inputs before sending
import { isAddress, zeroAddress, type Address, type Hex } from "viem";
import { AmountFormatError, toMinimalUnits } from "./format";
import type { ProjectConfigV3Input } from "./wallet";

/** RWAFactory.version() values the console can encode a deploy for */
export type FactoryVersion = "rwa-v2" | "rwa-v3";

/** Returns the supported factory version, or null for an empty or unknown value */
export function parseFactoryVersion(
  value: string | undefined,
): FactoryVersion | null {
  return value === "rwa-v2" || value === "rwa-v3" ? value : null;
}

export type StrategyChoice = "Fixed" | "Oracle";

// IRWAFactory.StrategyKind enum values
const STRATEGY_CHOICE_VALUE = { Fixed: 0, Oracle: 1 } as const;

// Bounds from OraclePriceStrategy: purchase spread up to 100%, redemption spread below it
const MAX_PURCHASE_SPREAD_BPS = 10_000;
const MAX_REDEMPTION_SPREAD_BPS = 9_999;
const MAX_UINT64 = (1n << 64n) - 1n;

/** rwa-v3 deploy form values, with amounts and oracle integers kept as input strings */
export interface DeployValuesV3 {
  name: string;
  symbol: string;
  quoteToken: string;
  strategyKind: StrategyChoice;
  purchasePrice: string;
  redemptionPrice: string;
  minPurchase: string;
  priceFeed: string;
  priceMaxAge: string;
  purchaseSpreadBps: string;
  redemptionSpreadBps: string;
  sequencerUptimeFeed: string;
  sequencerGracePeriod: string;
  redemptionTimeout: number;
  admin: string;
  auditor: string;
  complianceOperator: string;
  pricer: string;
  treasurer: string;
  redemptionManager: string;
  incomeManager: string;
  treasury: string;
  adminTransferDelay: number;
}

/** Identity fields from the stored profile, plus the quote token's on-chain decimals */
export interface DeployContext {
  decimals: number;
  profileDigest: Hex;
  projectId: Hex;
  quoteDecimals: number;
}

export type BuildResult = { config: ProjectConfigV3Input } | { error: string };

function address(label: string, value: string): Address | string {
  if (!isAddress(value, { strict: false }) || value === zeroAddress) {
    return `${label} must be a nonzero address.`;
  }
  return value as Address;
}

function uint(
  label: string,
  value: string,
  min: bigint,
  max: bigint,
): bigint | string {
  if (!/^\d+$/.test(value.trim())) {
    return `${label} must be a whole number.`;
  }
  const n = BigInt(value.trim());
  if (n < min || n > max) {
    return `${label} must be between ${min} and ${max}.`;
  }
  return n;
}

/**
 * Checks per-strategy inputs, scales quote amounts and returns ABI-ordered config with unused
 * fields zeroed. Fixed needs a pricer and positive prices; Oracle needs a feed and max age,
 * with sequencer feed and grace period set together.
 */
export function buildProjectConfigV3(
  v: DeployValuesV3,
  ctx: DeployContext,
): BuildResult {
  const required: [string, string][] = [
    ["Quote token", v.quoteToken],
    ["Admin", v.admin],
    ["Auditor", v.auditor],
    ["Compliance operator", v.complianceOperator],
    ["Treasurer", v.treasurer],
    ["Redemption manager", v.redemptionManager],
    ["Income manager", v.incomeManager],
    ["Treasury", v.treasury],
  ];
  const resolved: Record<string, Address> = {};
  for (const [label, value] of required) {
    const r = address(label, value);
    if (!isAddress(r, { strict: false })) return { error: r };
    resolved[label] = r;
  }

  let minPurchaseQuote: bigint;
  let purchase = 0n;
  let redemption = 0n;
  try {
    minPurchaseQuote = BigInt(
      toMinimalUnits(v.minPurchase.trim() || "0", ctx.quoteDecimals),
    );
    if (v.strategyKind === "Fixed") {
      purchase = BigInt(toMinimalUnits(v.purchasePrice, ctx.quoteDecimals));
      redemption = BigInt(toMinimalUnits(v.redemptionPrice, ctx.quoteDecimals));
    }
  } catch (err) {
    return {
      error:
        err instanceof AmountFormatError
          ? err.message
          : "Invalid amount - enter a whole-quote-token amount.",
    };
  }

  let pricer: Address = zeroAddress;
  let priceFeed: Address = zeroAddress;
  let priceMaxAge = 0n;
  let purchaseSpreadBps = 0n;
  let redemptionSpreadBps = 0n;
  let sequencerUptimeFeed: Address = zeroAddress;
  let sequencerGracePeriod = 0n;

  if (v.strategyKind === "Fixed") {
    const p = address("Pricer", v.pricer);
    if (!isAddress(p, { strict: false })) return { error: p };
    pricer = p;
    if (purchase === 0n || redemption === 0n) {
      return {
        error:
          "A fixed strategy needs a purchase and redemption price above 0.",
      };
    }
  } else {
    const feed = address("Price feed", v.priceFeed);
    if (!isAddress(feed, { strict: false })) return { error: feed };
    priceFeed = feed;
    const maxAge = uint("Max feed age", v.priceMaxAge, 1n, MAX_UINT64);
    if (typeof maxAge === "string") return { error: maxAge };
    priceMaxAge = maxAge;
    const ps = uint(
      "Purchase spread",
      v.purchaseSpreadBps || "0",
      0n,
      BigInt(MAX_PURCHASE_SPREAD_BPS),
    );
    if (typeof ps === "string") return { error: ps };
    purchaseSpreadBps = ps;
    const rs = uint(
      "Redemption spread",
      v.redemptionSpreadBps || "0",
      0n,
      BigInt(MAX_REDEMPTION_SPREAD_BPS),
    );
    if (typeof rs === "string") return { error: rs };
    redemptionSpreadBps = rs;

    const hasFeed = v.sequencerUptimeFeed.trim() !== "";
    const hasGrace =
      v.sequencerGracePeriod.trim() !== "" &&
      v.sequencerGracePeriod.trim() !== "0";
    if (hasFeed !== hasGrace) {
      return {
        error:
          "Set the sequencer uptime feed and the grace period together, or leave both empty.",
      };
    }
    if (hasFeed) {
      const seq = address("Sequencer uptime feed", v.sequencerUptimeFeed);
      if (!isAddress(seq, { strict: false })) return { error: seq };
      sequencerUptimeFeed = seq;
      const grace = uint(
        "Sequencer grace period",
        v.sequencerGracePeriod,
        1n,
        MAX_UINT64,
      );
      if (typeof grace === "string") return { error: grace };
      sequencerGracePeriod = grace;
    }
  }

  return {
    config: {
      name: v.name,
      symbol: v.symbol,
      decimals: ctx.decimals,
      profileDigest: ctx.profileDigest,
      projectId: ctx.projectId,
      quoteToken: resolved["Quote token"],
      purchasePricePerWholeToken: purchase,
      redemptionPricePerWholeToken: redemption,
      minPurchaseQuote,
      strategyKind: STRATEGY_CHOICE_VALUE[v.strategyKind],
      priceFeed,
      priceMaxAge,
      purchaseSpreadBps: Number(purchaseSpreadBps),
      redemptionSpreadBps: Number(redemptionSpreadBps),
      sequencerUptimeFeed,
      sequencerGracePeriod,
      redemptionTimeout: BigInt(v.redemptionTimeout),
      admin: resolved["Admin"],
      auditor: resolved["Auditor"],
      complianceOperator: resolved["Compliance operator"],
      pricer,
      treasurer: resolved["Treasurer"],
      redemptionManager: resolved["Redemption manager"],
      incomeManager: resolved["Income manager"],
      treasury: resolved["Treasury"],
      adminTransferDelay: v.adminTransferDelay,
    },
  };
}
