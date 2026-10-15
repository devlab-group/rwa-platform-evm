// Resolves RWA/quote decimals for lib/format.ts whole/minimal-unit conversion
import type { components } from "./api-types";
import { readErc20Decimals } from "./wallet";

type Project = components["schemas"]["Project"];

/** Uses Project.decimals or falls back to ERC-20 decimals() */
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
 * Uses cached Project.quoteDecimals or ERC-20 decimals() on addresses.quoteToken;
 * quote scaling is independent of RWA Project.decimals
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
