// Resolves RWA/quote decimals for lib/format.ts whole/minimal-unit conversion
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
 * Use Project.quoteDecimals or ERC-20 decimals() on quoteToken; never scale quote amounts with RWA
 * decimals
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
