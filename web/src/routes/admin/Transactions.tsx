import { AsyncSection } from "../../components/AsyncSection";
import { PaginationFooter } from "../../components/PaginationFooter";
import { TransactionStatusBadge } from "../../components/StatusBadge";
import { usePaginatedList } from "../../hooks/usePaginatedList";
import { api } from "../../lib/client";
import type { components } from "../../lib/api-types";
import { needsAttention, transactionStatusLabel } from "../../lib/status";

type Transaction = components["schemas"]["Transaction"];

/** Transactions: pending/final/reorged transactions and explorer links. */
export function Transactions() {
  const transactions = usePaginatedList<Transaction>(
    (cursor, signal) => api.listTransactions({ signal, cursor }),
    [],
  );

  return (
    <div>
      <header className="app-main__header">
        <h1>Transactions</h1>
        <p>
          Indexer-observed transactions. Unconfirmed and reorged states are
          called out.
        </p>
      </header>

      <section className="card">
        <AsyncSection
          state={transactions}
          onRetry={transactions.reload}
          empty={(d) => d.length === 0}
          emptyLabel="No transactions indexed yet."
        >
          {(data) => {
            const flagged = data.filter(
              (tx) => tx.status && needsAttention(tx.status),
            );
            return (
              <>
                {flagged.length > 0 && (
                  <div
                    className="async-state async-state--error u-mt-4"
                    role="alert"
                  >
                    <p>
                      {flagged.length} transaction
                      {flagged.length === 1 ? "" : "s"} need
                      {flagged.length === 1 ? "s" : ""} operator attention —
                      automated progress is stopped or unsafe. These reverted or
                      failed on-chain, had their block dropped by a reorg, have
                      an ambiguous broadcast outcome that must not be blindly
                      resubmitted, had their nonce consumed elsewhere, or
                      exhausted automatic replacement and require manual
                      intervention.
                    </p>
                    <ul className="u-mt-2">
                      {flagged.map((tx) => (
                        <li key={tx.txHash}>
                          <span className="badge badge--danger">
                            {tx.status
                              ? transactionStatusLabel(tx.status)
                              : "Unknown"}
                          </span>{" "}
                          <span className="mono">{tx.txHash}</span>
                          {tx.kind ? ` · ${tx.kind}` : ""}
                          {tx.blockNumber != null
                            ? ` · block ${tx.blockNumber}`
                            : ""}
                          {tx.explorerUrl && (
                            <>
                              {" · "}
                              <a
                                href={tx.explorerUrl}
                                target="_blank"
                                rel="noopener noreferrer"
                              >
                                explorer
                              </a>
                            </>
                          )}
                          {/* Operator-intervention detail: sender identity and
                              replacement lineage, shown only when the server
                              populates these optional fields. */}
                          {(tx.sender != null ||
                            tx.nonce != null ||
                            tx.replacedBy != null ||
                            tx.replacementCount != null) && (
                            <dl className="tx-intervention-detail">
                              {tx.nonce != null && (
                                <>
                                  <dt>Nonce</dt>
                                  <dd className="mono">{tx.nonce}</dd>
                                </>
                              )}
                              {tx.sender != null && (
                                <>
                                  <dt>Sender</dt>
                                  <dd className="mono">{tx.sender}</dd>
                                </>
                              )}
                              {tx.replacedBy != null && (
                                <>
                                  <dt>Replaced by</dt>
                                  <dd className="mono">{tx.replacedBy}</dd>
                                </>
                              )}
                              {tx.replacementCount != null && (
                                <>
                                  <dt>Replacements</dt>
                                  <dd>{tx.replacementCount}</dd>
                                </>
                              )}
                            </dl>
                          )}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>Tx hash</th>
                        <th>Kind</th>
                        <th>Status</th>
                        <th>Block</th>
                        <th>Explorer</th>
                      </tr>
                    </thead>
                    <tbody>
                      {data.map((tx) => (
                        <tr key={tx.txHash}>
                          <td className="mono">{tx.txHash}</td>
                          <td>{tx.kind ?? "—"}</td>
                          <td>
                            {tx.status && (
                              <TransactionStatusBadge status={tx.status} />
                            )}
                          </td>
                          <td>{tx.blockNumber ?? "—"}</td>
                          <td>
                            {tx.explorerUrl && (
                              <a
                                href={tx.explorerUrl}
                                target="_blank"
                                rel="noopener noreferrer"
                              >
                                View
                              </a>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                <PaginationFooter
                  loadedCount={data.length}
                  totalCount={transactions.totalCount}
                  hasMore={transactions.hasMore}
                  loadingMore={transactions.loadingMore}
                  loadMoreError={transactions.loadMoreError}
                  onLoadMore={transactions.loadMore}
                />
              </>
            );
          }}
        </AsyncSection>
      </section>
    </div>
  );
}
