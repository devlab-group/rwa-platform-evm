/**
 * Shown under a paginated list: says when the list stops at the server's page
 * size, and offers an explicit "Load more" button instead of loading every page
 * automatically. When the server omits `totalCount`, it shows only the loaded
 * count and a "more available" notice, not an invented total.
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
