import { useState } from "react";
import type { Address } from "viem";
import { AsyncSection } from "../../components/AsyncSection";
import { ConfirmStep } from "../../components/ConfirmStep";
import { PaginationFooter } from "../../components/PaginationFooter";
import { RoleGate } from "../../components/RoleGate";
import { TableWrap } from "../../components/TableWrap";
import { useWalletContext } from "../../context/walletContextValue";
import { useAsync } from "../../hooks/useAsync";
import { usePaginatedList } from "../../hooks/usePaginatedList";
import type { components } from "../../lib/api-types";
import { api, ApiError } from "../../lib/client";
import { resolveQuoteDecimals } from "../../lib/decimals";
import { formatTokenAmount, formatUnixSeconds } from "../../lib/format";
import {
  buildPrepareRequest,
  canReclaim,
  createDistributionBatch,
  deadlineSendError,
  extensionError,
  minPrepareDeadline,
  parseLocalDeadline,
  toCreateInput,
  toLocalInputValue,
} from "../../lib/income";
import { safeHttpsHref } from "../../lib/legal";
import { ROLES } from "../../lib/roles";
import { exportSafeBatch } from "../../lib/safeBatch";
import { useTxRoute } from "../../lib/txRoute";
import {
  describeWalletError,
  extendClaimDeadlineCall,
  findOnChainDistribution,
  readErc20Allowance,
  sendCreateDistribution,
  sendErc20Approve,
  sendExtendClaimDeadline,
  sendReclaimUnclaimed,
  waitForTxReceipt,
} from "../../lib/wallet";

type Project = components["schemas"]["Project"];
type Distribution = components["schemas"]["IncomeDistribution"];

const STATUS_TONE: Record<Distribution["status"], string> = {
  Prepared: "badge--info",
  Active: "badge--success",
  Expired: "badge--warning",
  Reclaimed: "badge--neutral",
};

const nowSeconds = () => Math.floor(Date.now() / 1000);

/** Prepares income snapshots, sends wallet/Safe funding, and lists deadline/reclaim actions */
export function Income() {
  const project = useAsync<Project>((signal) => api.getProject({ signal }), []);
  const proj = project.status === "success" ? project.data : undefined;
  const chainId = proj?.chainId ?? 0;

  // Quote amounts scale by the quote token's decimals, never the RWA token's
  const quoteDecimalsState = useAsync<number | null>(async () => {
    if (!proj || !chainId) return null;
    try {
      return await resolveQuoteDecimals(proj, chainId);
    } catch {
      return null;
    }
  }, [proj?.quoteDecimals, proj?.addresses?.quoteToken, chainId]);
  const quoteDecimals =
    quoteDecimalsState.status === "success"
      ? (quoteDecimalsState.data ?? undefined)
      : undefined;

  const list = usePaginatedList<Distribution>(
    (cursor, signal) => api.listIncomeDistributions({ cursor, signal }),
    [],
  );
  // The distribution being sent on-chain and the one being extended
  const [sending, setSending] = useState<Distribution | null>(null);
  const [extending, setExtending] = useState<Distribution | null>(null);
  // Outcome of the last send or extend, shown after its panel closes
  const [notice, setNotice] = useState<string | null>(null);

  const supported = proj?.version === "rwa-v3";

  return (
    <div>
      <header className="app-main__header">
        <h1>Income</h1>
        <p>
          Prepare quote-token income from a past holder snapshot, then fund it
          from the income manager's wallet or Safe.
        </p>
      </header>

      <AsyncSection state={project} onRetry={project.reload}>
        {(data) =>
          data.version === "rwa-v3" ? null : (
            <p className="async-state--error" role="alert">
              Income distribution needs an rwa-v3 project
              {data.version ? ` (this one is ${data.version})` : ""}.
            </p>
          )
        }
      </AsyncSection>

      {notice && <p role="status">{notice}</p>}

      {supported && proj && (
        <>
          <section className="card">
            <h2>Prepare a distribution</h2>
            <PrepareForm
              quoteDecimals={quoteDecimals}
              project={proj}
              chainId={chainId}
              onPrepared={(d) => {
                setSending(d);
                list.reload();
              }}
            />
          </section>

          {sending && (
            <section className="card">
              <h2>Send distribution on-chain</h2>
              <RoleGate
                roles={proj.roles}
                rolesByContract={proj.rolesByContract}
                contracts={["incomeDistributor"]}
                role={ROLES.incomeManager}
                action="create the distribution"
              >
                <SendPanel
                  key={sending.preparationId}
                  distribution={sending}
                  project={proj}
                  chainId={chainId}
                  quoteDecimals={quoteDecimals}
                  onDone={(message) => {
                    setSending(null);
                    setNotice(message);
                    list.reload();
                  }}
                  onCancel={() => setSending(null)}
                />
              </RoleGate>
            </section>
          )}

          {extending && (
            <section className="card">
              <h2>Extend claim deadline</h2>
              <RoleGate
                roles={proj.roles}
                rolesByContract={proj.rolesByContract}
                contracts={["incomeDistributor"]}
                role={ROLES.incomeManager}
                action="extend the claim deadline"
              >
                <ExtendPanel
                  key={extending.preparationId}
                  distribution={extending}
                  project={proj}
                  chainId={chainId}
                  onDone={(message) => {
                    setExtending(null);
                    setNotice(message);
                    list.reload();
                  }}
                  onCancel={() => setExtending(null)}
                />
              </RoleGate>
            </section>
          )}

          <section className="card">
            <h2>Distributions</h2>
            <AsyncSection
              state={list}
              onRetry={list.reload}
              empty={(d) => d.length === 0}
              emptyLabel="No distributions yet."
            >
              {(data) => (
                <>
                  <TableWrap label="Income distributions">
                    <table>
                      <thead>
                        <tr>
                          <th>ID</th>
                          <th>Status</th>
                          <th>Total</th>
                          <th>Claimed</th>
                          <th>Holders</th>
                          <th>Record block</th>
                          <th>Claim deadline</th>
                          <th>Snapshot</th>
                          <th>Snapshot SHA-256</th>
                          <th>Actions</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.map((d) => (
                          <DistributionRow
                            key={`${d.preparationId}:${d.distributionId ?? ""}`}
                            distribution={d}
                            project={proj}
                            chainId={chainId}
                            quoteDecimals={quoteDecimals}
                            onSend={() => setSending(d)}
                            onExtend={() => setExtending(d)}
                            onReclaimed={() => {
                              setNotice(
                                "Unclaimed income reclaimed to the treasury.",
                              );
                              list.reload();
                            }}
                          />
                        ))}
                      </tbody>
                    </table>
                  </TableWrap>
                  <PaginationFooter
                    loadedCount={data.length}
                    totalCount={list.totalCount}
                    hasMore={list.hasMore}
                    loadingMore={list.loadingMore}
                    loadMoreError={list.loadMoreError}
                    onLoadMore={list.loadMore}
                  />
                </>
              )}
            </AsyncSection>
          </section>
        </>
      )}
    </div>
  );
}

function PrepareForm({
  project,
  chainId,
  quoteDecimals,
  onPrepared,
}: {
  project: Project;
  chainId: number;
  quoteDecimals: number | undefined;
  onPrepared: (d: Distribution) => void;
}) {
  const [recordBlock, setRecordBlock] = useState("");
  const [total, setTotal] = useState("");
  const [deadline, setDeadline] = useState(() =>
    toLocalInputValue(minPrepareDeadline(nowSeconds()) + 24 * 3600),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  async function handle() {
    setError(null);
    setDone(null);
    let decimals = quoteDecimals;
    if (decimals === undefined) {
      try {
        decimals = await resolveQuoteDecimals(project, chainId);
      } catch {
        setError(
          "Could not read quote-token decimals. Connect your wallet to the project chain and retry.",
        );
        return;
      }
    }
    const built = buildPrepareRequest(
      {
        recordBlock,
        total,
        deadlineSeconds: parseLocalDeadline(deadline),
      },
      decimals,
      nowSeconds(),
    );
    if ("error" in built) {
      setError(built.error);
      return;
    }
    setBusy(true);
    try {
      const prepared = await api.prepareIncomeDistribution(
        built.request,
        `income-${built.request.recordBlock}-${crypto.randomUUID()}`,
      );
      setDone(
        `Snapshot prepared with ${prepared.holderCount} holder(s). Review and send it on-chain below.`,
      );
      onPrepared(prepared);
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err.message
          : "Could not prepare the snapshot.",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div>
      <p className="field__hint">
        Entitlements use end-of-block RWA balances, including escrowed tokens.
        The server pins the snapshot to IPFS; the income manager funds it on-chain.
      </p>
      <div className="field">
        <label htmlFor="income-record-block">Record block</label>
        <input
          id="income-record-block"
          inputMode="numeric"
          value={recordBlock}
          onChange={(e) => setRecordBlock(e.target.value)}
          aria-describedby="income-record-block-hint"
        />
        <span className="field__hint" id="income-record-block-hint">
          A finalized block, before the one that will mine the creation.
        </span>
      </div>
      <div className="field">
        <label htmlFor="income-total">
          Total to distribute (whole quote-token units)
        </label>
        <input
          id="income-total"
          value={total}
          onChange={(e) => setTotal(e.target.value)}
          aria-describedby="income-total-hint"
        />
        <span className="field__hint" id="income-total-hint">
          Paid by the income manager's wallet or Safe. Rounding dust stays in
          the contract until reclaimed.
        </span>
      </div>
      <div className="field">
        <label htmlFor="income-deadline">Claim deadline</label>
        <input
          id="income-deadline"
          type="datetime-local"
          value={deadline}
          onChange={(e) => setDeadline(e.target.value)}
          aria-describedby="income-deadline-hint"
        />
        <span className="field__hint" id="income-deadline-hint">
          At least 7 days and 1 hour from now. After it, unclaimed income can be
          reclaimed to the treasury.
        </span>
      </div>
      <button
        type="button"
        className="button button--primary"
        onClick={handle}
        disabled={busy || !recordBlock || !total || !deadline}
      >
        {busy ? "Preparing…" : "Prepare distribution"}
      </button>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {done && <p role="status">{done}</p>}
    </div>
  );
}

function Summary({
  distribution: d,
  quoteDecimals,
  chainId,
  distributor,
  quoteToken,
}: {
  distribution: Distribution;
  quoteDecimals: number | undefined;
  chainId: number;
  distributor: string | undefined;
  quoteToken: string | undefined;
}) {
  return (
    <dl className="tx-preview__grid">
      <div className="tx-preview__row">
        <dt>Chain ID</dt>
        <dd>{chainId}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Income distributor</dt>
        <dd className="mono">{distributor ?? "-"}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Quote token</dt>
        <dd className="mono">{quoteToken ?? "-"}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Total</dt>
        <dd>
          {formatTokenAmount(d.total, quoteDecimals)} ({d.total} minimal units)
        </dd>
      </div>
      <div className="tx-preview__row">
        <dt>Record block</dt>
        <dd>{d.recordBlock}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Claim deadline</dt>
        <dd>{formatUnixSeconds(d.claimDeadline)}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Merkle root</dt>
        <dd className="mono">{d.merkleRoot}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Snapshot</dt>
        <dd className="mono">{d.snapshotURI}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Snapshot SHA-256</dt>
        <dd className="mono">{d.snapshotSha256}</dd>
      </div>
    </dl>
  );
}

// Approve funding if needed, then createDistribution; a Safe batch includes both calls
function SendPanel({
  distribution: d,
  project,
  chainId,
  quoteDecimals,
  onDone,
  onCancel,
}: {
  distribution: Distribution;
  project: Project;
  chainId: number;
  quoteDecimals: number | undefined;
  onDone: (message: string) => void;
  onCancel: () => void;
}) {
  const route = useTxRoute();
  const from = useWalletContext().address;
  const [busy, setBusy] = useState(false);
  const [step, setStep] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const distributor = project.addresses?.incomeDistributor;
  const quoteToken = project.addresses?.quoteToken;

  async function handle() {
    setError(null);
    setDone(null);
    if (!distributor || !quoteToken) {
      setError("Project addresses unavailable. Reload once it is deployed.");
      return;
    }
    if (d.status !== "Prepared") {
      setError("This distribution is already on-chain.");
      return;
    }
    const created = toCreateInput(d);
    if ("error" in created) {
      setError(created.error);
      return;
    }
    const tooClose = deadlineSendError(d.claimDeadline, nowSeconds());
    if (tooClose) {
      setError(tooClose);
      return;
    }
    if (route.kind === "wallet" && !from) {
      setError("Connect the income manager's wallet.");
      return;
    }
    setBusy(true);
    let approved = false;
    try {
      // Check the contract before indexing catches up. Safe exports need no injected wallet,
      // so warn owners if the check is unavailable; wallet sends must pass it.
      setStep("Checking for an existing distribution of this snapshot…");
      let existing: bigint | null = null;
      let unchecked: string | null = null;
      try {
        existing = await findOnChainDistribution(
          chainId,
          distributor as Address,
          created.input.merkleRoot,
          created.input.snapshotSha256,
        );
      } catch (err) {
        if (route.kind !== "safe") {
          setError(
            `Could not check the income distributor for this snapshot: ${describeWalletError(err)}`,
          );
          return;
        }
        unchecked = describeWalletError(err);
      }
      if (existing !== null) {
        setError(
          `Distribution ${existing} already pays this snapshot on-chain. Creating it again would pay the income twice; the list shows it once the server indexes it.`,
        );
        return;
      }
      if (route.kind === "safe") {
        const saved = exportSafeBatch(
          chainId,
          route.safe,
          "Create income distribution",
          createDistributionBatch(
            quoteToken as Address,
            distributor as Address,
            created.input,
          ),
        );
        setDone(
          unchecked === null
            ? saved
            : `${saved} The console could not check the income distributor for an existing distribution of this snapshot (${unchecked}). Before executing, confirm that no distribution on ${distributor} has merkle root ${created.input.merkleRoot} and snapshot hash ${created.input.snapshotSha256}.`,
        );
        return;
      }
      if (!from) return;
      // A retry after a failed create reuses the approval already in place
      setStep("Checking the quote-token allowance…");
      const allowance = await readErc20Allowance(
        chainId,
        quoteToken as Address,
        from,
        distributor as Address,
      );
      if (allowance < created.input.total) {
        setStep("Approving the quote token…");
        await waitForTxReceipt(
          await sendErc20Approve(
            chainId,
            from,
            quoteToken as Address,
            distributor as Address,
            created.input.total,
          ),
        );
      }
      approved = true;
      setStep("Creating the distribution…");
      await waitForTxReceipt(
        await sendCreateDistribution(
          chainId,
          from,
          distributor as Address,
          created.input,
        ),
      );
      onDone(
        "Distribution created. Waiting for the server to index the event.",
      );
    } catch (err) {
      setError(
        approved
          ? `The approval went through but createDistribution failed: ${describeWalletError(err)} The approval stays in place; a retry reuses it, or revoke it.`
          : describeWalletError(err),
      );
    } finally {
      setBusy(false);
      setStep(null);
    }
  }

  return (
    <div>
      <ConfirmStep
        reviewLabel="Review and send"
        confirmLabel={
          route.kind === "safe" ? "Save Safe batch" : "Approve and create"
        }
        confirmHeading="Confirm income distribution"
        confirmWarning={
          route.kind === "safe"
            ? "Saves a Safe batch: approve the exact total, then createDistribution. Safe owners review and execute it. Execute only one batch per distribution; the contract accepts duplicate snapshots and would pay again."
            : "Approves quote tokens if needed, then sends createDistribution from your wallet. The server sends neither transaction."
        }
        submitting={busy}
        onConfirm={handle}
      >
        <Summary
          distribution={d}
          quoteDecimals={quoteDecimals}
          chainId={chainId}
          distributor={distributor}
          quoteToken={quoteToken}
        />
      </ConfirmStep>{" "}
      <button
        type="button"
        className="button button--secondary"
        onClick={onCancel}
        disabled={busy}
      >
        Close
      </button>
      {step && <p role="status">{step}</p>}
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {done && <p role="status">{done}</p>}
    </div>
  );
}

function ExtendPanel({
  distribution: d,
  project,
  chainId,
  onDone,
  onCancel,
}: {
  distribution: Distribution;
  project: Project;
  chainId: number;
  onDone: (message: string) => void;
  onCancel: () => void;
}) {
  const route = useTxRoute();
  const from = useWalletContext().address;
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const distributor = project.addresses?.incomeDistributor;

  async function handle() {
    setError(null);
    setDone(null);
    const next = parseLocalDeadline(value);
    const invalid = extensionError(d.claimDeadline, next, nowSeconds());
    if (invalid || next === null) {
      setError(invalid);
      return;
    }
    if (!distributor || d.distributionId === undefined) {
      setError("This distribution is not on-chain yet.");
      return;
    }
    const call = extendClaimDeadlineCall(
      distributor as Address,
      BigInt(d.distributionId),
      BigInt(next),
    );
    if (route.kind === "safe") {
      setDone(
        exportSafeBatch(chainId, route.safe, "Extend claim deadline", [call]),
      );
      return;
    }
    if (!from) {
      setError("Connect the income manager's wallet.");
      return;
    }
    setBusy(true);
    try {
      await waitForTxReceipt(
        await sendExtendClaimDeadline(
          chainId,
          from,
          distributor as Address,
          BigInt(d.distributionId),
          BigInt(next),
        ),
      );
      onDone("Claim deadline extended.");
    } catch (err) {
      setError(describeWalletError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div>
      <p className="field__hint">
        Distribution {d.distributionId}: current deadline{" "}
        {formatUnixSeconds(d.claimDeadline)}. Deadlines can only move later;
        extending also works while the project is paused.
      </p>
      <div className="field">
        <label htmlFor="income-new-deadline">New claim deadline</label>
        <input
          id="income-new-deadline"
          type="datetime-local"
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
      </div>
      <button
        type="button"
        className="button button--primary"
        onClick={handle}
        disabled={busy || !value}
      >
        {busy ? "Submitting…" : "Extend deadline"}
      </button>{" "}
      <button
        type="button"
        className="button button--secondary"
        onClick={onCancel}
        disabled={busy}
      >
        Close
      </button>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {done && <p role="status">{done}</p>}
    </div>
  );
}

function DistributionRow({
  distribution: d,
  project,
  chainId,
  quoteDecimals,
  onSend,
  onExtend,
  onReclaimed,
}: {
  distribution: Distribution;
  project: Project;
  chainId: number;
  quoteDecimals: number | undefined;
  onSend: () => void;
  onExtend: () => void;
  onReclaimed: () => void;
}) {
  const from = useWalletContext().address;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const distributor = project.addresses?.incomeDistributor;
  const href = safeHttpsHref(d.snapshotURI);
  const onChain = d.distributionId !== undefined;
  const paused = Boolean(project.paused);
  const reclaimable = canReclaim(d, paused, nowSeconds());
  const label = d.distributionId ?? d.preparationId.slice(0, 8);

  async function reclaim() {
    setError(null);
    if (!from || !distributor || d.distributionId === undefined) {
      setError("Connect a wallet to reclaim.");
      return;
    }
    setBusy(true);
    try {
      await waitForTxReceipt(
        await sendReclaimUnclaimed(
          chainId,
          from,
          distributor as Address,
          BigInt(d.distributionId),
        ),
      );
      onReclaimed();
    } catch (err) {
      setError(describeWalletError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <tr>
      <td className="mono" title={d.preparationId}>
        {label}
      </td>
      <td>
        <span className={`badge ${STATUS_TONE[d.status]}`}>{d.status}</span>
      </td>
      <td>{formatTokenAmount(d.total, quoteDecimals)}</td>
      <td>
        {d.claimed === undefined
          ? "-"
          : formatTokenAmount(d.claimed, quoteDecimals)}
      </td>
      <td>{d.holderCount}</td>
      <td>{d.recordBlock}</td>
      <td>{formatUnixSeconds(d.claimDeadline)}</td>
      <td className="mono">
        {href ? (
          <a href={href} target="_blank" rel="noopener noreferrer">
            {d.snapshotURI}
          </a>
        ) : (
          d.snapshotURI
        )}
      </td>
      <td className="mono">{d.snapshotSha256}</td>
      <td>
        {d.status === "Prepared" && (
          <button
            type="button"
            className="button button--secondary"
            aria-label={`Send distribution ${label} on-chain`}
            onClick={onSend}
          >
            Send on-chain
          </button>
        )}
        {(d.status === "Active" || d.status === "Expired") && onChain && (
          <button
            type="button"
            className="button button--secondary"
            aria-label={`Extend deadline of distribution ${label}`}
            onClick={onExtend}
          >
            Extend deadline
          </button>
        )}
        {onChain && d.status !== "Reclaimed" && (
          <button
            type="button"
            className="button button--secondary"
            aria-label={`Reclaim unclaimed income of distribution ${label}`}
            onClick={reclaim}
            disabled={busy || !reclaimable}
            title={
              paused
                ? "Reclaim is blocked while the project is paused"
                : reclaimable
                  ? undefined
                  : "Available after the claim deadline"
            }
          >
            {busy ? "Submitting…" : "Reclaim unclaimed"}
          </button>
        )}
        {error && (
          <p className="async-state--error" role="alert">
            {error}
          </p>
        )}
      </td>
    </tr>
  );
}
