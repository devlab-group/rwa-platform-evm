// Resolves the decimals to use when converting a human-entered amount to
// minimal units (or rendering a minimal-unit amount back as whole units) for a
// given token — the second half of the req_changes Web 1 boundary, alongside
// lib/format.ts's toMinimalUnits/formatTokenAmount.
import type { components } from "./api-types";
import { readErc20Decimals } from "./wallet";

type Project = components["schemas"]["Project"];

/**
 * RWA token decimals: Project.decimals when the server provides it
 * (authoritative), else an on-chain ERC-20 decimals() read for older server
 * builds that omit the field (the same fallback BalanceSection uses).
 */
export async function resolveRwaDecimals(
  project: Project | undefined,
  chainId: number,
): Promise<number> {
  if (project?.decimals !== undefined) return project.decimals;
  const token = project?.addresses?.token;
  if (!token || !chainId) {
    throw new Error("Token decimals unavailable — project not fully loaded.");
  }
  return readErc20Decimals(chainId, token as `0x${string}`);
}

/**
 * Quote-token decimals: Project.quoteDecimals when the server provides it
 * (authoritative — resolved once on-chain at deploy and stored with the
 * project), else an on-chain ERC-20 decimals() read of addresses.quoteToken
 * for older server builds that omit the field. The quote token is a separate
 * stablecoin whose decimals (often 6) differ from the RWA token's, so it must
 * never be scaled with Project.decimals.
 */
export async function resolveQuoteDecimals(
  project: Project | undefined,
  chainId: number,
): Promise<number> {
  if (project?.quoteDecimals !== undefined) return project.quoteDecimals;
  const quoteToken = project?.addresses?.quoteToken;
  if (!quoteToken || !chainId) {
    throw new Error(
      "Quote token decimals unavailable — project not fully loaded.",
    );
  }
  return readErc20Decimals(chainId, quoteToken as `0x${string}`);
}
