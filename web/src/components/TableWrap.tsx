import type { ReactNode } from "react";

/**
 * Labelled, focusable region for keyboard scrolling when a table overflows
 * horizontally (axe rule scrollable-region-focusable)
 */
export function TableWrap({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="table-wrap" role="region" aria-label={label} tabIndex={0}>
      {children}
    </div>
  );
}
