import { useAsync } from "./useAsync";
import type { components } from "../lib/api-types";
import { resolveQuoteDecimals } from "../lib/decimals";

type Project = components["schemas"]["Project"];

/**
 * Read quote-token decimals for list rows. While loading or unavailable, return undefined so
 * rows display grouped raw units without guessing their scale.
 */
export function useQuoteDecimals(
  project: Project | undefined,
  chainId: number,
): number | undefined {
  const state = useAsync<number | null>(async () => {
    if (!project || !chainId) return null;
    try {
      return await resolveQuoteDecimals(project, chainId);
    } catch {
      return null;
    }
  }, [project?.addresses?.quoteToken, chainId]);
  return state.status === "success" ? (state.data ?? undefined) : undefined;
}
