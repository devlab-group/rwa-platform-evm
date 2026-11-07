import { useAsync } from "./useAsync";
import type { components } from "../lib/api-types";
import { resolveQuoteDecimals } from "../lib/decimals";

type Project = components["schemas"]["Project"];

/**
 * Return the project's quote-token decimals immediately, or resolve them on-chain.
 * Return undefined while loading or after a failed read so rows label amounts in minimal units.
 */
export function useQuoteDecimals(
  project: Project | undefined,
  chainId: number,
): number | undefined {
  const known = project?.quoteDecimals;
  const state = useAsync<number | null>(async () => {
    if (!project || !chainId) return null;
    try {
      return await resolveQuoteDecimals(project, chainId);
    } catch {
      return null;
    }
  }, [project?.addresses?.quoteToken, chainId, known]);
  if (known !== undefined) return known;
  return state.status === "success" ? (state.data ?? undefined) : undefined;
}
