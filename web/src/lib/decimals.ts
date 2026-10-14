// Resolves the decimals for converting an entered amount to minimal units, or
// a minimal-unit amount to whole units, for a given token. Pairs with
// toMinimalUnits and formatTokenAmount in lib/format.ts.
import type { components } from "./api-types";
import { readErc20Decimals } from "./wallet";

type Project = components["schemas"]["Project"];

/** RWA token decimals: Project.decimals when present, else an on-chain ERC-20 decimals() read */
export async function resolveRwaDecimals(
  project: Project | undefined,
  chainId: number,
): Promise<number> {
  if (project?.decimals !== undefined) return project.decimals;
  const token = project?.addresses?.token;
  if (!token || !chainId) {
    throw new Error("Token decimals unavailable - project not fully loaded.");
  }
  return readErc20Decimals(chainId, token as `0x${string}`);
}

/**
 * Quote-token decimals: Project.quoteDecimals when present, else an on-chain
 * ERC-20 decimals() read of addresses.quoteToken. The quote token is a separate
 * ERC-20 whose decimals can differ from the RWA token's, so never scale it with
 * Project.decimals.
 */
export async function resolveQuoteDecimals(
  project: Project | undefined,
  chainId: number,
): Promise<number> {
  if (project?.quoteDecimals !== undefined) return project.quoteDecimals;
  const quoteToken = project?.addresses?.quoteToken;
  if (!quoteToken || !chainId) {
    throw new Error(
      "Quote token decimals unavailable - project not fully loaded.",
    );
  }
  return readErc20Decimals(chainId, quoteToken as `0x${string}`);
}
