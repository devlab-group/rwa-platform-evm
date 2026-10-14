// Resolves the decimals used to convert a human-entered amount to minimal units,
// or a minimal-unit amount back to whole units, for a token. Used with
// toMinimalUnits and formatTokenAmount in lib/format.ts.
import type { components } from "./api-types";
import { readErc20Decimals } from "./wallet";

type Project = components["schemas"]["Project"];

/**
 * RWA token decimals: Project.decimals when the response has it, else an
 * on-chain ERC-20 decimals() read.
 */
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
 * Quote-token decimals: Project.quoteDecimals when the response has it (the
 * server reads it on-chain once at project setup), else an on-chain ERC-20
 * decimals() read of addresses.quoteToken. The quote token is a separate token
 * whose decimals can differ from the RWA token's, so it is never scaled with
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
