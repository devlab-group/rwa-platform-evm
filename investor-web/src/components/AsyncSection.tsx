import type { ReactNode } from "react";
import type { AsyncState } from "../hooks/useAsync";

interface Props<T> {
  state: AsyncState<T>;
  onRetry?: () => void;
  empty?: (data: T) => boolean;
  emptyLabel?: string;
  children: (data: T) => ReactNode;
}

/** Renders one of loading / error / empty / content for any useAsync() result. */
export function AsyncSection<T>({
  state,
  onRetry,
  empty,
  emptyLabel,
  children,
}: Props<T>) {
  if (state.status === "loading") {
    return (
      <div className="async-state async-state--loading" role="status">
        Loading…
      </div>
    );
  }
  if (state.status === "error") {
    return (
      <div className="async-state async-state--error" role="alert">
        <p>{state.error}</p>
        {onRetry && (
          <button
            type="button"
            className="button button--secondary"
            onClick={onRetry}
          >
            Retry
          </button>
        )}
      </div>
    );
  }
  if (empty?.(state.data)) {
    return (
      <div className="async-state async-state--empty">
        {emptyLabel ?? "Nothing here yet."}
      </div>
    );
  }
  return <>{children(state.data)}</>;
}
