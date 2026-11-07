import { useState } from "react";
import type { Address } from "viem";
import { AsyncSection } from "../../components/AsyncSection";
import { PaginationFooter } from "../../components/PaginationFooter";
import { RoleGate } from "../../components/RoleGate";
import { PurchaseOrderStatusBadge } from "../../components/StatusBadge";
import { useWalletContext } from "../../context/walletContextValue";
import { useAsync } from "../../hooks/useAsync";
import { usePaginatedList } from "../../hooks/usePaginatedList";
import { api } from "../../lib/client";
import type { components } from "../../lib/api-types";
import { resolveQuoteDecimals } from "../../lib/decimals";
import { orderPricingMessage, withdrawableQuote } from "../../lib/purchase";
import { ROLES } from "../../lib/roles";
import { exportSafeBatch } from "../../lib/safeBatch";
import { useTxRoute, type TxRoute } from "../../lib/txRoute";
import {
  formatTokenAmount,
  formatUnixSeconds,
  shortenAddress,
  toMinimalUnits,
} from "../../lib/format";
import {
  readOrderPricing,
  readPreviewBuy,
  rejectPurchaseCall,
  sendRejectPurchase,
  sendSettlePurchase,
  sendWithdrawProceeds,
  withdrawProceedsCall,
  waitForTxReceipt,
  type OrderPricing,
} from "../../lib/wallet";
import { TableWrap } from "../../components/TableWrap";

type Inventory = components["schemas"]["Inventory"];
type Project = components["schemas"]["Project"];
type Purchase = components["schemas"]["Purchase"];
type PurchaseOrder = components["schemas"]["PurchaseOrder"];

const ORDER_STATUS_FILTERS = [
  "",
  "Pending",
  "Settled",
  "Refunded",
  "Cancelled",
  "Rejected",
] as const;

// Vault purchase preview in RWA and quote-token minimal units
interface BuyQuote {
  tokenAmount: string;
  quoteAmount: string;
}

/**
 * Shows inventory, quotes and purchases; withdraw through a role-holding wallet or Safe. Instant
 * strategies sell through Vault.buy; forward-priced ones fill orders at a later feed round, which
 * anyone can settle and the treasurer can reject while the project is paused.
 */
export function InventorySales() {
  const inventory = useAsync<Inventory>(
    (signal) => api.getInventory({ signal }),
    [],
  );
  const project = useAsync<Project>((signal) => api.getProject({ signal }), []);
  const purchases = usePaginatedList<Purchase>(
    (cursor, signal) => api.listPurchases({ signal, cursor }),
    [],
  );

  const proj = project.status === "success" ? project.data : undefined;
  const chainId = proj?.chainId ?? 0;
  // Oracle projects on rwa-v4 fill orders at a later round and never offer Vault.buy
  const forwardPriced = proj?.forwardPriced === true;
  const showOrders = forwardPriced || proj?.version === "rwa-v4";
  // Resolve RWA/quote decimals separately; display may use raw units, submission requires a known
  // scale
  const rwaDecimals = proj?.decimals;
  const quoteDecimalsState = useAsync<number | null>(async () => {
    if (!proj || !chainId) return null;
    try {
      return await resolveQuoteDecimals(proj, chainId);
    } catch {
      return null;
    }
  }, [proj?.addresses?.quoteToken, chainId]);
  const quoteDecimals =
    quoteDecimalsState.status === "success"
      ? (quoteDecimalsState.data ?? undefined)
      : undefined;

  return (
    <div>
      <header className="app-main__header">
        <h1>Inventory &amp; Sales</h1>
        <p>Vault inventory, pricing, purchases, and treasury withdrawal.</p>
      </header>

      <section className="card">
        <h2>Inventory</h2>
        <AsyncSection state={inventory} onRetry={inventory.reload}>
          {(data) => (
            <dl className="tx-preview__grid">
              <div className="tx-preview__row">
                <dt>Vault inventory</dt>
                <dd>{formatTokenAmount(data.inventory ?? "", rwaDecimals)}</dd>
              </div>
              <div className="tx-preview__row">
                <dt>Quote balance</dt>
                <dd>
                  {formatTokenAmount(data.quoteBalance ?? "", quoteDecimals)}
                </dd>
              </div>
              {data.pendingPurchaseQuote !== undefined && (
                <>
                  <div className="tx-preview__row">
                    <dt>Held for open purchase orders</dt>
                    <dd>
                      {formatTokenAmount(
                        data.pendingPurchaseQuote,
                        quoteDecimals,
                      )}
                    </dd>
                  </div>
                  <div className="tx-preview__row">
                    <dt>Withdrawable</dt>
                    <dd>
                      {formatTokenAmount(
                        withdrawableQuote(data)?.toString() ?? "",
                        quoteDecimals,
                      )}
                    </dd>
                  </div>
                </>
              )}
              <div className="tx-preview__row">
                <dt>
                  {forwardPriced
                    ? "Latest purchase price (indicative)"
                    : "Purchase price"}
                </dt>
                <dd>
                  {formatTokenAmount(data.purchasePrice ?? "", quoteDecimals)}
                </dd>
              </div>
              <div className="tx-preview__row">
                <dt>
                  {forwardPriced
                    ? "Latest redemption price (indicative)"
                    : "Redemption price"}
                </dt>
                <dd>
                  {formatTokenAmount(data.redemptionPrice ?? "", quoteDecimals)}
                </dd>
              </div>
              {data.priceError && (
                <div className="tx-preview__row">
                  <dt>Price status</dt>
                  <dd className="async-state--error" role="alert">
                    Unavailable: the oracle strategy reports {data.priceError}.{" "}
                    {forwardPriced
                      ? "Quote previews revert until the feed recovers; orders and redemption requests are still accepted and price at a later round."
                      : "Purchases and redemption requests revert until the feed recovers."}
                  </dd>
                </div>
              )}
              {project.status === "success" &&
                project.data.addresses?.strategy && (
                  <div className="tx-preview__row">
                    <dt>Strategy address</dt>
                    <dd title={project.data.addresses.strategy}>
                      {shortenAddress(project.data.addresses.strategy)}
                    </dd>
                  </div>
                )}
            </dl>
          )}
        </AsyncSection>
      </section>

      <section className="card">
        <h2>
          {forwardPriced
            ? "Indicative purchase quote"
            : "Purchase quote preview"}
        </h2>
        <BuyQuotePreview
          vault={proj?.addresses?.vault}
          chainId={chainId}
          rwaDecimals={rwaDecimals}
          quoteDecimals={quoteDecimals}
          forwardPriced={forwardPriced}
          dealingCutoff={proj?.oracle?.dealingCutoff}
        />
      </section>

      <section className="card">
        <h2>Treasury withdrawal</h2>
        <RoleGate
          roles={proj?.roles}
          rolesByContract={proj?.rolesByContract}
          contracts={["vault"]}
          role={ROLES.treasurer}
          action="withdraw treasury proceeds"
        >
          <WithdrawForm
            project={proj}
            chainId={chainId}
            availableQuote={
              inventory.status === "success"
                ? withdrawableQuote(inventory.data)?.toString()
                : undefined
            }
            quoteBalance={
              inventory.status === "success"
                ? inventory.data.quoteBalance
                : undefined
            }
            pendingQuote={
              inventory.status === "success"
                ? inventory.data.pendingPurchaseQuote
                : undefined
            }
            quoteDecimals={quoteDecimals}
            onWithdrawn={inventory.reload}
          />
        </RoleGate>
      </section>

      {showOrders && (
        <PurchaseOrders
          project={proj}
          chainId={chainId}
          rwaDecimals={rwaDecimals}
          quoteDecimals={quoteDecimals}
          onChanged={inventory.reload}
        />
      )}

      <section className="card">
        <h2>Purchase history</h2>
        <AsyncSection
          state={purchases}
          onRetry={purchases.reload}
          empty={(d) => d.length === 0}
          emptyLabel="No purchases recorded yet."
        >
          {(data) => (
            <>
              <TableWrap label="Purchase history">
                <table>
                  <thead>
                    <tr>
                      <th>Tx hash</th>
                      <th>Buyer</th>
                      <th>Recipient</th>
                      <th>Token amount</th>
                      <th>Quote amount</th>
                      <th>Confirmations</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.map((p) => (
                      <tr key={p.txHash}>
                        <td className="mono">{p.txHash}</td>
                        <td className="mono" title={p.buyer}>
                          {shortenAddress(p.buyer)}
                        </td>
                        <td className="mono" title={p.recipient}>
                          {shortenAddress(p.recipient)}
                        </td>
                        <td>
                          {formatTokenAmount(p.tokenAmount ?? "", rwaDecimals)}
                        </td>
                        <td>
                          {formatTokenAmount(
                            p.quoteAmount ?? "",
                            quoteDecimals,
                          )}
                        </td>
                        <td>{p.confirmations ?? 0}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
              <PaginationFooter
                loadedCount={data.length}
                totalCount={purchases.totalCount}
                hasMore={purchases.hasMore}
                loadingMore={purchases.loadingMore}
                loadMoreError={purchases.loadMoreError}
                onLoadMore={purchases.loadMore}
              />
            </>
          )}
        </AsyncSection>
      </section>
    </div>
  );
}

function BuyQuotePreview({
  vault,
  chainId,
  rwaDecimals,
  quoteDecimals,
  forwardPriced,
  dealingCutoff,
}: {
  vault: string | undefined;
  chainId: number;
  rwaDecimals: number | undefined;
  quoteDecimals: number | undefined;
  forwardPriced: boolean;
  dealingCutoff: number | undefined;
}) {
  const [tokenAmount, setTokenAmount] = useState("");
  const [quote, setQuote] = useState<BuyQuote | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function handlePreview() {
    if (rwaDecimals === undefined) {
      setError("Token decimals not available yet - project still loading.");
      return;
    }
    if (!vault) {
      setError("Missing vault address - project not deployed?");
      return;
    }
    setLoading(true);
    setError(null);
    try {
      // Convert RWA units before reading the Vault's live purchase preview
      const minimal = toMinimalUnits(tokenAmount, rwaDecimals);
      const quoteAmount = await readPreviewBuy(
        chainId,
        vault as Address,
        BigInt(minimal),
      );
      setQuote({ tokenAmount: minimal, quoteAmount: quoteAmount.toString() });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Quote request failed.");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div>
      {forwardPriced && (
        <p className="field__hint">
          This project prices orders at a later feed round
          {dealingCutoff
            ? `, published at least ${dealingCutoff} s after the order`
            : ""}
          , so Vault.buy is disabled and this figure is the feed&apos;s latest
          answer, not what an order will pay. A buyer requests an order with a
          maximum deposit; anyone can then settle it once that round lands.
        </p>
      )}
      <div className="field">
        <label htmlFor="buy-amount">RWA token amount (whole units)</label>
        <input
          id="buy-amount"
          value={tokenAmount}
          onChange={(e) => setTokenAmount(e.target.value)}
        />
      </div>
      <button
        type="button"
        className="button button--secondary"
        onClick={handlePreview}
        disabled={loading || !tokenAmount}
      >
        {loading ? "Fetching…" : "Preview quote"}
      </button>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {quote && (
        <dl className="tx-preview__grid u-mt-4">
          <div className="tx-preview__row">
            <dt>Token amount</dt>
            <dd>{formatTokenAmount(quote.tokenAmount, rwaDecimals)}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Quote amount</dt>
            <dd>{formatTokenAmount(quote.quoteAmount, quoteDecimals)}</dd>
          </div>
          <div className="tx-preview__row">
            {/* Vault.previewBuy quotes purchases */}
            <dt>Side</dt>
            <dd>
              {forwardPriced
                ? "purchase (indicative, not binding)"
                : "purchase"}
            </dd>
          </div>
        </dl>
      )}
    </div>
  );
}

function WithdrawForm({
  project,
  chainId,
  availableQuote,
  quoteBalance,
  pendingQuote,
  quoteDecimals,
  onWithdrawn,
}: {
  project: Project | undefined;
  chainId: number;
  // Quote balance minus open order deposits, in minimal units; the bound for the amount
  availableQuote: string | undefined;
  // The two figures behind availableQuote, shown when open order deposits are ring-fenced
  quoteBalance: string | undefined;
  pendingQuote: string | undefined;
  // Quote decimals for display and early balance validation, when available
  quoteDecimals: number | undefined;
  onWithdrawn: () => void;
}) {
  const from = useWalletContext().address;
  const route = useTxRoute();
  const [amount, setAmount] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [progress, setProgress] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Validate early with known quote decimals; resolve and recheck balance at submission
  const overBalance =
    availableQuote !== undefined &&
    quoteDecimals !== undefined &&
    amount !== "" &&
    (() => {
      try {
        return (
          BigInt(toMinimalUnits(amount, quoteDecimals)) > BigInt(availableQuote)
        );
      } catch {
        return false; // report malformed input at submission
      }
    })();

  async function handleWithdraw() {
    setError(null);
    setDone(null);
    setProgress(null);
    if (route.kind === "wallet" && !from) {
      setError("Wallet not connected.");
      return;
    }
    const vault = project?.addresses?.vault;
    if (!vault) {
      setError("Missing vault address - project not deployed?");
      return;
    }
    // Resolve quote-token decimals before scaling the withdrawal amount
    let minimal: string;
    try {
      const decimals = await resolveQuoteDecimals(project, chainId);
      minimal = toMinimalUnits(amount, decimals);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Invalid amount.");
      return;
    }
    // Check reported quote balance before submit/export; the contract enforces live state
    if (
      availableQuote !== undefined &&
      BigInt(minimal) > BigInt(availableQuote)
    ) {
      setError(
        `Amount exceeds the vault's available quote balance (${formatTokenAmount(
          availableQuote,
          quoteDecimals,
        )}).`,
      );
      return;
    }
    if (route.kind === "safe") {
      setDone(
        exportSafeBatch(chainId, route.safe, "Withdraw proceeds", [
          withdrawProceedsCall(vault as Address, BigInt(minimal)),
        ]),
      );
      return;
    }
    if (!from) return;
    setBusy(true);
    try {
      setProgress("Submitting withdrawal…");
      const hash = await sendWithdrawProceeds(
        chainId,
        from,
        vault as Address,
        BigInt(minimal),
      );
      await waitForTxReceipt(hash);
      setDone("Withdrawal submitted.");
      onWithdrawn();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Withdrawal failed.");
    } finally {
      setBusy(false);
      setProgress(null);
    }
  }

  return (
    <div>
      <div className="field">
        <label htmlFor="withdraw-amount">
          Amount (whole quote-token units)
        </label>
        <input
          id="withdraw-amount"
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
          aria-describedby="withdraw-amount-hint"
        />
        <span className="field__hint" id="withdraw-amount-hint">
          Available in vault:{" "}
          {availableQuote !== undefined
            ? formatTokenAmount(availableQuote, quoteDecimals)
            : "-"}
          {pendingQuote !== undefined && quoteBalance !== undefined
            ? ` (balance ${formatTokenAmount(quoteBalance, quoteDecimals)} minus ${formatTokenAmount(pendingQuote, quoteDecimals)} held for open purchase orders)`
            : ""}
          . Cannot exceed this.
        </span>
        {overBalance && (
          <span className="async-state--error" role="alert">
            Amount exceeds the vault&apos;s available quote balance.
          </span>
        )}
      </div>
      <button
        type="button"
        className="button button--primary"
        onClick={handleWithdraw}
        disabled={busy || !amount || overBalance}
      >
        {busy ? "Submitting…" : "Withdraw"}
      </button>
      {progress && (
        <p role="status" className="u-mt-4">
          {progress}
        </p>
      )}
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {done && (
        <p role="status" className="u-mt-4">
          {done}
        </p>
      )}
    </div>
  );
}

// Reads what a Pending order would settle at, on the order's own strategy; other orders need no read
function useOrderPricing(order: PurchaseOrder | undefined, chainId: number) {
  return useAsync<OrderPricing | null>(async () => {
    if (!order || order.status !== "Pending" || !chainId) return null;
    return readOrderPricing(
      chainId,
      order.strategy as Address,
      BigInt(order.tokenAmount),
      BigInt(order.baseRound),
      BigInt(order.createdAt),
    );
  }, [order?.id, order?.status, chainId]);
}

function PurchaseOrders({
  project,
  chainId,
  rwaDecimals,
  quoteDecimals,
  onChanged,
}: {
  project: Project | undefined;
  chainId: number;
  rwaDecimals: number | undefined;
  quoteDecimals: number | undefined;
  onChanged: () => void;
}) {
  const [statusFilter, setStatusFilter] =
    useState<(typeof ORDER_STATUS_FILTERS)[number]>("");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const orders = usePaginatedList<PurchaseOrder>(
    (cursor, signal) =>
      api.listPurchaseOrders({
        signal,
        cursor,
        status: statusFilter || undefined,
      }),
    [statusFilter],
  );

  return (
    <>
      <section className="card">
        <h2>Purchase orders</h2>
        <p className="field__hint">
          Each order holds the buyer&apos;s deposit in the Vault until it
          settles at the first feed round published after its dealing cut-off,
          is refunded, or is cancelled or rejected.
        </p>
        <div className="field field--narrow">
          <label htmlFor="order-status-filter">Filter orders by status</label>
          <select
            id="order-status-filter"
            value={statusFilter}
            onChange={(e) =>
              setStatusFilter(
                e.target.value as (typeof ORDER_STATUS_FILTERS)[number],
              )
            }
          >
            {ORDER_STATUS_FILTERS.map((s) => (
              <option key={s} value={s}>
                {s || "All"}
              </option>
            ))}
          </select>
        </div>
        <AsyncSection
          state={orders}
          onRetry={orders.reload}
          empty={(d) => d.length === 0}
          emptyLabel="No purchase orders match this filter."
        >
          {(data) => (
            <>
              <TableWrap label="Purchase orders">
                <table>
                  <thead>
                    <tr>
                      <th>ID</th>
                      <th>Buyer</th>
                      <th>RWA amount</th>
                      <th>Deposit</th>
                      <th>Status</th>
                      <th>Price</th>
                      <th>Timeout</th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.map((o) => (
                      <tr key={o.id}>
                        <td className="mono">{o.id}</td>
                        <td className="mono" title={o.buyer}>
                          {shortenAddress(o.buyer)}
                        </td>
                        <td>{formatTokenAmount(o.tokenAmount, rwaDecimals)}</td>
                        <td>
                          {formatTokenAmount(o.maxQuoteAmount, quoteDecimals)}
                        </td>
                        <td>
                          <PurchaseOrderStatusBadge status={o.status} />
                        </td>
                        <td>
                          <OrderPriceCell
                            order={o}
                            chainId={chainId}
                            quoteDecimals={quoteDecimals}
                          />
                        </td>
                        <td>{formatUnixSeconds(o.timeoutAt)}</td>
                        <td>
                          <button
                            type="button"
                            className="button button--secondary"
                            onClick={() => setSelectedId(o.id)}
                          >
                            Manage
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
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
      </section>

      {selectedId && (
        <OrderDetail
          id={selectedId}
          project={project}
          chainId={chainId}
          rwaDecimals={rwaDecimals}
          quoteDecimals={quoteDecimals}
          onChanged={() => {
            orders.reload();
            onChanged();
          }}
          onClose={() => setSelectedId(null)}
        />
      )}
    </>
  );
}

// Short price text for a table row: what a Pending order would settle at, or what closed it
function OrderPriceCell({
  order,
  chainId,
  quoteDecimals,
}: {
  order: PurchaseOrder;
  chainId: number;
  quoteDecimals: number | undefined;
}) {
  const pricing = useOrderPricing(order, chainId);
  if (order.status !== "Pending") {
    return (
      <>
        {order.pricedRound ? `Round ${order.pricedRound}` : "No round"}
        {order.refund
          ? `, refund ${formatTokenAmount(order.refund, quoteDecimals)}`
          : ""}
      </>
    );
  }
  if (pricing.status === "loading") return <>Reading…</>;
  if (pricing.status === "error") {
    return <span title={pricing.error}>Price not read</span>;
  }
  switch (pricing.data?.state) {
    case "priced":
      return (
        <>
          {formatTokenAmount(
            pricing.data.quoteAmount.toString(),
            quoteDecimals,
          )}
        </>
      );
    case "waiting":
      return <>Waiting for a round</>;
    case "blocked":
      return <span title={pricing.data.message}>Round unusable</span>;
    default:
      return <>-</>;
  }
}

function OrderDetail({
  id,
  project,
  chainId,
  rwaDecimals,
  quoteDecimals,
  onChanged,
  onClose,
}: {
  id: string;
  project: Project | undefined;
  chainId: number;
  rwaDecimals: number | undefined;
  quoteDecimals: number | undefined;
  onChanged: () => void;
  onClose: () => void;
}) {
  const from = useWalletContext().address;
  const detail = useAsync<PurchaseOrder>(() => api.getPurchaseOrder(id), [id]);
  const order = detail.status === "success" ? detail.data : undefined;
  const pricing = useOrderPricing(order, chainId);
  const [busy, setBusy] = useState<"settle" | "reject" | null>(null);
  const [progress, setProgress] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const paused = project?.paused === true;
  const fmtQuote = (raw: string | bigint) =>
    formatTokenAmount(String(raw), quoteDecimals);
  const pricingState =
    pricing.status === "success" ? pricing.data?.state : undefined;
  // Settlement reverts without a usable round, so the button waits for one
  const settleBlockReason = paused
    ? "Settlement is refused while the project is paused."
    : pricingState === "waiting"
      ? "No price round has landed yet."
      : pricingState === "blocked"
        ? "The pricing round cannot be used."
        : undefined;

  function begin() {
    setError(null);
    setDone(null);
    setProgress(null);
  }

  // Anyone can settle: no role, only a connected wallet
  async function handleSettle() {
    begin();
    const vault = project?.addresses?.vault;
    if (!from) {
      setError("Wallet not connected.");
      return;
    }
    if (!vault) {
      setError("Missing vault address - project not deployed?");
      return;
    }
    setBusy("settle");
    try {
      setProgress("Settling order…");
      const hash = await sendSettlePurchase(
        chainId,
        from,
        vault as Address,
        BigInt(id),
      );
      await waitForTxReceipt(hash);
      setDone(
        "Settlement confirmed. The order shows Settled, or Refunded if its price is outside the buyer's bounds, once indexed.",
      );
      detail.reload();
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Settlement failed.");
    } finally {
      setBusy(null);
      setProgress(null);
    }
  }

  async function handleReject(route: TxRoute) {
    begin();
    const vault = project?.addresses?.vault;
    if (route.kind === "wallet" && !from) {
      setError("Wallet not connected.");
      return;
    }
    if (!vault) {
      setError("Missing vault address - project not deployed?");
      return;
    }
    if (route.kind === "safe") {
      setDone(
        exportSafeBatch(chainId, route.safe, "Reject purchase order", [
          rejectPurchaseCall(vault as Address, BigInt(id)),
        ]),
      );
      return;
    }
    if (!from) return;
    setBusy("reject");
    try {
      setProgress("Rejecting order…");
      const hash = await sendRejectPurchase(
        chainId,
        from,
        vault as Address,
        BigInt(id),
      );
      await waitForTxReceipt(hash);
      setDone("Order rejected. The whole deposit was returned to the buyer.");
      detail.reload();
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Rejection failed.");
    } finally {
      setBusy(null);
      setProgress(null);
    }
  }

  return (
    <section className="card">
      <h2>Order {id}</h2>
      <AsyncSection state={detail} onRetry={detail.reload}>
        {(o) => (
          <dl className="tx-preview__grid">
            <div className="tx-preview__row">
              <dt>Status</dt>
              <dd>
                <PurchaseOrderStatusBadge status={o.status} />
              </dd>
            </div>
            <div className="tx-preview__row">
              <dt>Buyer</dt>
              <dd className="mono">{o.buyer}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Recipient</dt>
              <dd className="mono">{o.recipient}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>RWA amount</dt>
              <dd>{formatTokenAmount(o.tokenAmount, rwaDecimals)}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Deposit (buyer&apos;s maximum)</dt>
              <dd>{fmtQuote(o.maxQuoteAmount)}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Pricing strategy</dt>
              <dd className="mono">{o.strategy}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Base round</dt>
              <dd className="mono">{o.baseRound}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Created</dt>
              <dd>{formatUnixSeconds(o.createdAt)}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Buyer can cancel from</dt>
              <dd>{formatUnixSeconds(o.timeoutAt)}</dd>
            </div>
            {o.status === "Pending" ? (
              <div className="tx-preview__row">
                <dt>Price</dt>
                <dd>
                  {pricing.status === "loading" && "Reading…"}
                  {pricing.status === "error" &&
                    `Could not read the price: ${pricing.error}`}
                  {pricing.status === "success" && pricing.data
                    ? orderPricingMessage(pricing.data, fmtQuote)
                    : null}
                </dd>
              </div>
            ) : (
              <>
                <div className="tx-preview__row">
                  <dt>Priced at round</dt>
                  <dd className="mono">{o.pricedRound ?? "-"}</dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Quote at that round</dt>
                  <dd>{o.quoteAmount ? fmtQuote(o.quoteAmount) : "-"}</dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Refunded to buyer</dt>
                  <dd>{o.refund ? fmtQuote(o.refund) : "-"}</dd>
                </div>
              </>
            )}
          </dl>
        )}
      </AsyncSection>

      {order?.status === "Pending" && (
        <div className="tx-preview__actions u-mt-4">
          <button
            type="button"
            className="button button--primary"
            onClick={handleSettle}
            disabled={busy !== null || settleBlockReason !== undefined}
            title={settleBlockReason}
          >
            {busy === "settle" ? "Submitting…" : "Settle order"}
          </button>
          {paused ? (
            <RoleGate
              roles={project?.roles}
              rolesByContract={project?.rolesByContract}
              contracts={["vault"]}
              role={ROLES.treasurer}
              action="reject purchase orders"
            >
              {(route) => (
                <>
                  <p className="field__hint">
                    Rejecting returns the buyer&apos;s whole deposit of{" "}
                    {fmtQuote(order.maxQuoteAmount)} and closes the order as
                    Rejected. The buyer receives no RWA.
                  </p>
                  <button
                    type="button"
                    className="button button--danger"
                    onClick={() => handleReject(route)}
                    disabled={busy !== null}
                  >
                    {busy === "reject"
                      ? "Submitting…"
                      : route.kind === "safe"
                        ? "Save rejection batch"
                        : "Reject order"}
                  </button>
                </>
              )}
            </RoleGate>
          ) : (
            <p className="field__hint">
              Orders can be rejected only while the project is paused. Pausing
              also stops settlement, so a bad round can be caught before anyone
              settles at it.
            </p>
          )}
        </div>
      )}

      <div className="tx-preview__actions u-mt-4">
        <button
          type="button"
          className="button button--secondary"
          onClick={onClose}
        >
          Close
        </button>
      </div>

      {progress && (
        <p role="status" className="u-mt-4">
          {progress}
        </p>
      )}
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {done && (
        <p role="status" className="u-mt-4">
          {done}
        </p>
      )}
    </section>
  );
}
