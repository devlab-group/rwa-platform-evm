import { useState } from "react";
import type { Address } from "viem";
import { AsyncSection } from "../../components/AsyncSection";
import { PaginationFooter } from "../../components/PaginationFooter";
import { useAsync } from "../../hooks/useAsync";
import { useNowSeconds } from "../../hooks/useNowSeconds";
import { usePaginatedList } from "../../hooks/usePaginatedList";
import { useQuoteDecimals } from "../../hooks/useQuoteDecimals";
import { api } from "../../lib/client";
import type { components } from "../../lib/api-types";
import { formatTokenAmount, formatUnixSeconds } from "../../lib/format";
import { minPurchaseQuote } from "../../lib/v3";
import {
  formatDuration,
  isForwardPriced,
  orderActions,
  orderReadArgs,
  parseUint,
  settlementOutlook,
  type OrderNote,
  type OrderRound,
} from "../../lib/v4";
import {
  describeTxError,
  readOrderMinQuote,
  readOrderRound,
  sendCancelPurchase,
  sendSettlePurchase,
  waitForTxReceipt,
} from "../../lib/wallet";

type Project = components["schemas"]["Project"];
type PurchaseOrder = components["schemas"]["PurchaseOrder"];

const ADDRESS = /^0x[0-9a-fA-F]{40}$/;

// Appended to a column header whose decimals are unknown, so raw digits are never read as tokens
const unitNote = " (minimal units)";

const STATUS_TONE: Record<PurchaseOrder["status"], string> = {
  Pending: "badge--neutral",
  Settled: "badge--success",
  Refunded: "badge--warning",
  Cancelled: "badge--danger",
  Rejected: "badge--danger",
};

function noteText(note: OrderNote, timeoutAt: number): string {
  switch (note) {
    case "buyer-not-allowed":
      return "The buyer wallet is not Allowed in the compliance registry, so this order cannot be settled or cancelled until it is Allowed again.";
    case "paused":
      return "The project is paused. This order can be settled once it is unpaused.";
    case "unreadable":
      return "The price for this order could not be read. Refresh to try again.";
    case "cancel-opens-later":
      return `If no price arrives, you can cancel from ${formatUnixSeconds(timeoutAt)}.`;
    case "not-buyer":
      return "Only the buyer can cancel this order.";
  }
}

/**
 * Forward-priced purchase orders of the connected wallet. A Pending order's pricing round is
 * read on the order's own strategy; Settle and Cancel follow what that read and the order's
 * timeout allow (see lib/v4.ts). Hidden on instant projects unless the wallet still has orders.
 * Query orders whenever a wallet is connected, independently of the server's forward-pricing
 * flag. refreshKey reloads the list after an order is placed elsewhere on the page.
 */
export function PurchaseOrdersSection({
  project,
  walletAddress,
  chainMismatch,
  refreshKey,
  onSubmitted,
}: {
  project: Project | undefined;
  walletAddress: string | null | undefined;
  chainMismatch: boolean;
  refreshKey: number;
  onSubmitted: (txHash: string, kind: string) => void;
}) {
  const chainId = project?.chainId ?? 0;
  const vault = project?.addresses?.vault;
  const orders = usePaginatedList<PurchaseOrder>(
    (cursor, signal) =>
      walletAddress
        ? api.listPurchaseOrders({ address: walletAddress, cursor, signal })
        : Promise.resolve({ items: [] }),
    [walletAddress, refreshKey],
  );
  const hasOrders = orders.status === "success" && orders.data.length > 0;
  // Settle and cancel need an Allowed buyer; one lookup serves the whole list
  const buyer = useAsync<boolean | undefined>(
    async (signal) =>
      walletAddress && hasOrders
        ? (await api.isAddressAllowed(walletAddress, { signal })).allowed
        : undefined,
    [walletAddress, hasOrders],
  );
  const buyerAllowed = buyer.status === "success" ? buyer.data : undefined;
  const quoteDecimals = useQuoteDecimals(project, chainId);
  const minimum = minPurchaseQuote(project);
  const [sent, setSent] = useState<{ kind: string; hash: string } | null>(null);
  const [error, setError] = useState<string | null>(null);
  // One settle or cancel at a time, from wallet prompt until the receipt
  const [busy, setBusy] = useState(false);

  if (!isForwardPriced(project) && !hasOrders) return null;

  async function act(kind: "settle" | "cancel", id: string) {
    const orderId = parseUint(id);
    if (!walletAddress || !vault || !chainId || orderId === null) return;
    setError(null);
    setBusy(true);
    try {
      const send = kind === "settle" ? sendSettlePurchase : sendCancelPurchase;
      const hash = await send(
        chainId,
        walletAddress as Address,
        vault as Address,
        orderId,
      );
      setSent({ kind, hash });
      onSubmitted(hash, `purchase ${kind}`);
      await waitForTxReceipt(hash);
      orders.reload();
    } catch (err) {
      setError(
        describeTxError(
          err,
          kind === "settle" ? "Settle failed." : "Cancel failed.",
        ),
      );
    } finally {
      setBusy(false);
    }
  }

  const cutoff = formatDuration(project?.oracle?.dealingCutoff ?? 0);

  return (
    <section className="card" aria-label="Purchase orders">
      <h2>Purchase orders</h2>
      <p className="field__hint">
        An order is priced by the first feed price published at least{" "}
        {cutoff || "the dealing cut-off"} after you placed it. Anyone can settle
        a priced order: settling delivers the tokens and returns the unused
        deposit. A fill is refunded in full if the inventory runs out or the
        recipient stops being eligible first.
      </p>
      {!walletAddress ? (
        <p className="field__hint">
          Connect a wallet to view your purchase orders.
        </p>
      ) : (
        <>
          <button
            type="button"
            className="button button--secondary"
            onClick={orders.reload}
          >
            Refresh orders
          </button>
          <AsyncSection
            state={orders}
            onRetry={orders.reload}
            empty={(d) => d.length === 0}
            emptyLabel="No purchase orders for this wallet yet."
          >
            {(data) => (
              <>
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>ID</th>
                        <th>Status</th>
                        <th>
                          RWA amount
                          {project?.decimals === undefined && unitNote}
                        </th>
                        <th>
                          Deposit{quoteDecimals === undefined && unitNote}
                        </th>
                        <th>Price</th>
                        <th>Timeout</th>
                        <th>
                          <span className="visually-hidden">Actions</span>
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {data.map((order) => (
                        <OrderRow
                          key={order.id}
                          order={order}
                          project={project}
                          chainId={chainId}
                          walletAddress={walletAddress}
                          chainMismatch={chainMismatch}
                          busy={busy}
                          buyerAllowed={buyerAllowed}
                          quoteDecimals={quoteDecimals}
                          minimum={minimum}
                          onAct={act}
                        />
                      ))}
                    </tbody>
                  </table>
                </div>
                <PaginationFooter
                  loadedCount={data.length}
                  totalCount={orders.totalCount}
                  hasMore={orders.hasMore}
                  loadingMore={orders.loadingMore}
                  loadMoreError={orders.loadMoreError}
                  onLoadMore={orders.loadMore}
                />
              </>
            )}
          </AsyncSection>
        </>
      )}
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {sent && (
        <p role="status">
          {sent.kind === "settle" ? "Settle" : "Cancel"} submitted:{" "}
          <span className="mono">{sent.hash}</span>
        </p>
      )}
    </section>
  );
}

function OrderRow({
  order,
  project,
  chainId,
  walletAddress,
  chainMismatch,
  busy,
  buyerAllowed,
  quoteDecimals,
  minimum,
  onAct,
}: {
  order: PurchaseOrder;
  project: Project | undefined;
  chainId: number;
  walletAddress: string;
  chainMismatch: boolean;
  busy: boolean;
  buyerAllowed: boolean | undefined;
  quoteDecimals: number | undefined;
  minimum: bigint;
  onAct: (kind: "settle" | "cancel", id: string) => void;
}) {
  const pending = order.status === "Pending";
  const round = useAsync<OrderRound>(async () => {
    if (!pending) return { state: "unknown" };
    // Server values are untrusted: a malformed round, amount or id reads as unknown, never
    // throws, and offers no action
    const args = orderReadArgs(order);
    if (!args || !ADDRESS.test(order.strategy) || parseUint(order.id) === null)
      return { state: "unknown" };
    try {
      return await readOrderRound(chainId, order.strategy as Address, args);
    } catch {
      return { state: "unknown" };
    }
  }, [order.id, order.status, chainId, chainMismatch]);

  // Re-evaluates when the timeout passes
  const nowSeconds = useNowSeconds(pending ? [order.timeoutAt] : []);

  const loading = pending && round.status === "loading";
  const current: OrderRound =
    round.status === "success" ? round.data : { state: "unknown" };
  // Read the order's recorded minimum for the settlement preview; null falls back to the project minimum
  const vault = project?.addresses?.vault;
  const priced = current.state === "priced";
  const recorded = useAsync<bigint | null>(async () => {
    const id = parseUint(order.id);
    if (!priced || !vault || !ADDRESS.test(vault) || id === null) return null;
    try {
      return await readOrderMinQuote(chainId, vault as Address, id);
    } catch {
      return null;
    }
  }, [priced, order.id, vault, chainId, chainMismatch]);
  const orderMinimum: OrderMinimum =
    recorded.status !== "success"
      ? { kind: "loading" }
      : recorded.data === null
        ? { kind: "project", value: minimum }
        : { kind: "order", value: recorded.data };
  const actions = orderActions(order, current, {
    nowSeconds,
    paused: project?.paused,
    buyerAllowed,
    isBuyer: order.buyer?.toLowerCase() === walletAddress.toLowerCase(),
  });
  const money = (raw: string | undefined) =>
    formatTokenAmount(raw ?? "", quoteDecimals);
  // Raw amounts are named as minimal units, never as whole quote tokens
  const unit =
    quoteDecimals === undefined ? "quote-token minimal units" : "quote tokens";

  return (
    <tr>
      <td className="mono">{order.id}</td>
      <td>
        <span
          className={`badge ${STATUS_TONE[order.status] ?? "badge--neutral"}`}
          data-status={order.status}
        >
          {order.status}
        </span>
      </td>
      <td>{formatTokenAmount(order.tokenAmount ?? "", project?.decimals)}</td>
      <td>{money(order.maxQuoteAmount)}</td>
      <td>
        {pending ? (
          <>
            {loading && <p>Checking for a price…</p>}
            {current.state === "waiting" && <p>Waiting for a price.</p>}
            {current.state === "unusable" && (
              <p>
                The pricing round cannot price this order: {current.reason}.
              </p>
            )}
            {current.state === "priced" &&
              pricedText(current.quote, order, orderMinimum, money, unit)}
            {!loading && actions.note && (
              <p className="field__hint">
                {noteText(actions.note, order.timeoutAt)}
              </p>
            )}
          </>
        ) : (
          closedText(order, money, unit)
        )}
      </td>
      <td>{formatUnixSeconds(order.timeoutAt)}</td>
      <td>
        <span className="badge-row">
          {!loading && actions.settle && (
            <button
              type="button"
              className="button button--primary"
              onClick={() => onAct("settle", order.id)}
              disabled={chainMismatch || busy}
              aria-label={`Settle order ${order.id}`}
            >
              Settle
            </button>
          )}
          {!loading && actions.cancel && (
            <button
              type="button"
              className="button button--danger"
              onClick={() => onAct("cancel", order.id)}
              disabled={chainMismatch || busy}
              aria-label={`Cancel order ${order.id}`}
            >
              Cancel order
            </button>
          )}
        </span>
      </td>
    </tr>
  );
}

// Preview minimum: the order's recorded value, the project fallback after a failed read, or still loading
type OrderMinimum =
  | { kind: "loading" }
  | { kind: "order"; value: bigint }
  | { kind: "project"; value: bigint };

function pricedText(
  quote: bigint,
  order: PurchaseOrder,
  orderMinimum: OrderMinimum,
  money: (raw: string | undefined) => string,
  unit: string,
) {
  const deposit = parseUint(order.maxQuoteAmount);
  const quoted = money(quote.toString());
  if (deposit === null || orderMinimum.kind === "loading")
    return (
      <p>
        Priced at {quoted} {unit}.
      </p>
    );
  const minimum = orderMinimum.value;
  const outlook = settlementOutlook(quote, deposit, minimum);
  const fallbackNote = orderMinimum.kind === "project" && (
    <>
      {" "}
      The minimum recorded on the order could not be read, so the project&apos;s
      current minimum is used for this preview.
    </>
  );
  if (outlook.outcome === "fill") {
    return (
      <p>
        Priced at {quoted} {unit}. Settling pays that and returns{" "}
        {money(outlook.refund.toString())} of your deposit.
        {fallbackNote}
      </p>
    );
  }
  const owner = orderMinimum.kind === "order" ? "the order's" : "the project";
  return (
    <p>
      Priced at {quoted} {unit},{" "}
      {outlook.reason === "above-deposit"
        ? "above your deposit"
        : `below ${owner} minimum of ${money(minimum.toString())}`}
      . Settling refunds the whole deposit ({money(outlook.refund.toString())})
      and delivers no tokens.
      {fallbackNote}
    </p>
  );
}

function closedText(
  order: PurchaseOrder,
  money: (raw: string | undefined) => string,
  unit: string,
) {
  switch (order.status) {
    case "Settled":
      return (
        <p>
          Paid {money(order.quoteAmount)} {unit}, refunded {money(order.refund)}
          .
        </p>
      );
    case "Refunded":
      return (
        <p>
          Refunded {money(order.refund)} {unit}; no tokens delivered.
        </p>
      );
    default:
      return (
        <p>
          Refunded {money(order.refund)} {unit}.
        </p>
      );
  }
}
