import { useEffect, useRef, useState } from "react";
import { ApiError } from "../lib/client";
import type { PageResult } from "../lib/client";
import type { AsyncState } from "./useAsync";

function errorMessage(err: unknown): string {
  return err instanceof ApiError
    ? err.message
    : err instanceof Error
      ? err.message
      : "Request failed";
}

/**
 * Like useAsync, but for the cursor-paginated list endpoints. It fetches the
 * first page on mount and when `deps` change, and exposes `totalCount` and
 * `hasMore` from the X-Total-Count and X-Next-Cursor headers, so a screen can
 * show how much is loaded instead of silently stopping at the server's default
 * page size. `loadMore()` appends the next page to `data`. The state has the
 * same `{status, data, error}` shape as useAsync, so it works with
 * <AsyncSection>.
 */
export function usePaginatedList<T>(
  fetchPage: (
    cursor: string | undefined,
    signal: AbortSignal,
  ) => Promise<PageResult<T>>,
  deps: unknown[],
): AsyncState<T[]> & {
  reload: () => void;
  loadMore: () => void;
  loadingMore: boolean;
  loadMoreError: string | null;
  totalCount: number | undefined;
  hasMore: boolean;
} {
  const [state, setState] = useState<AsyncState<T[]>>({ status: "loading" });
  const [totalCount, setTotalCount] = useState<number | undefined>(undefined);
  const [nextCursor, setNextCursor] = useState<string | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadMoreError, setLoadMoreError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const fetchRef = useRef(fetchPage);
  fetchRef.current = fetchPage;

  useEffect(() => {
    const controller = new AbortController();
    setState({ status: "loading" });
    setTotalCount(undefined);
    setNextCursor(undefined);
    setLoadMoreError(null);
    fetchRef
      .current(undefined, controller.signal)
      .then((page) => {
        if (controller.signal.aborted) return;
        setState({ status: "success", data: page.items });
        setTotalCount(page.totalCount);
        setNextCursor(page.nextCursor);
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        setState({ status: "error", error: errorMessage(err) });
      });
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, reloadToken]);

  async function loadMore() {
    if (!nextCursor || state.status !== "success") return;
    setLoadingMore(true);
    setLoadMoreError(null);
    try {
      const page = await fetchRef.current(
        nextCursor,
        new AbortController().signal,
      );
      setState((prev) =>
        prev.status === "success"
          ? { status: "success", data: [...prev.data, ...page.items] }
          : prev,
      );
      setTotalCount(page.totalCount);
      setNextCursor(page.nextCursor);
    } catch (err) {
      // Keep the loaded items and cursor: a failed "load more" shouldn't blank
      // the list. The error is surfaced separately so the user can retry
      setLoadMoreError(errorMessage(err));
    } finally {
      setLoadingMore(false);
    }
  }

  return {
    ...state,
    reload: () => setReloadToken((n) => n + 1),
    loadMore,
    loadingMore,
    loadMoreError,
    totalCount,
    hasMore: Boolean(nextCursor),
  };
}
