import { useEffect, useRef, useState } from "react";
import { ApiError } from "../lib/client";

export type AsyncState<T> =
  | { status: "loading"; data?: undefined; error?: undefined }
  | { status: "error"; data?: undefined; error: string }
  | { status: "success"; data: T; error?: undefined };

/**
 * Runs `fetcher` on mount and whenever `deps` change, tracking loading/error/
 * success so every section renders consistent states against a not-yet-running
 * server (connection refused surfaces as an error state, not a crash).
 */
export function useAsync<T>(
  fetcher: (signal: AbortSignal) => Promise<T>,
  deps: unknown[],
): AsyncState<T> & { reload: () => void } {
  const [state, setState] = useState<AsyncState<T>>({ status: "loading" });
  const [reloadToken, setReloadToken] = useState(0);
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  useEffect(() => {
    const controller = new AbortController();
    setState({ status: "loading" });
    fetcherRef
      .current(controller.signal)
      .then((data) => {
        if (!controller.signal.aborted) setState({ status: "success", data });
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        const message =
          err instanceof ApiError
            ? err.message
            : err instanceof Error
              ? err.message
              : "Request failed";
        setState({ status: "error", error: message });
      });
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, reloadToken]);

  return { ...state, reload: () => setReloadToken((n) => n + 1) };
}
