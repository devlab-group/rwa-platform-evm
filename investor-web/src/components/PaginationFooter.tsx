/**
 * Shown under a paginated list: makes it visible when the list is truncated
 * at the server's page size rather than hiding older records silently, and
 * offers an explicit "Load more" action instead of unbounded auto-pagination.
 * `totalCount` is omitted by the server for most-recent-N stores; render a
 * plain "more available" notice in that case rather than a fabricated total.
 */
export function PaginationFooter({
  loadedCount,
  totalCount,
  hasMore,
  loadingMore,
  loadMoreError,
  onLoadMore,
}: {
  loadedCount: number;
  totalCount: number | undefined;
  hasMore: boolean;
  loadingMore: boolean;
  loadMoreError: string | null;
  onLoadMore: () => void;
}) {
  if (!hasMore && totalCount === undefined) return null;
  return (
    <div className="pagination-footer">
      <p className="field__hint">
        {totalCount !== undefined
          ? `Showing ${loadedCount} of ${totalCount}.`
          : `Showing ${loadedCount}.`}
        {hasMore && " More records are available."}
      </p>
      {hasMore && (
        <button
          type="button"
          className="button button--secondary"
          onClick={onLoadMore}
          disabled={loadingMore}
        >
          {loadingMore ? "Loading…" : "Load more"}
        </button>
      )}
      {loadMoreError && (
        <p className="async-state--error" role="alert">
          {loadMoreError}
        </p>
      )}
    </div>
  );
}
