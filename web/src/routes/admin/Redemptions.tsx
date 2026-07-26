import { useState } from "react";
import type { Address, Hex } from "viem";
import { AsyncSection } from "../../components/AsyncSection";
import { PaginationFooter } from "../../components/PaginationFooter";
import { RoleGate } from "../../components/RoleGate";
import {
  RedemptionComplianceBadge,
  RedemptionStatusBadge,
} from "../../components/StatusBadge";
import { useWalletContext } from "../../context/walletContextValue";
import { useAsync } from "../../hooks/useAsync";
import { usePaginatedList } from "../../hooks/usePaginatedList";
import { api } from "../../lib/client";
import type { components } from "../../lib/api-types";
import { resolveQuoteDecimals } from "../../lib/decimals";
import { encodeReasonCode } from "../../lib/redemption";
import { ROLES } from "../../lib/roles";
import {
  formatTokenAmount,
  formatUnixSeconds,
  shortenAddress,
} from "../../lib/format";
import { redemptionBlockedByCompliance } from "../../lib/status";
import {
  readErc20Allowance,
  sendErc20Approve,
  sendFundRedemption,
  sendRejectRedemption,
  waitForTxReceipt,
} from "../../lib/wallet";

type Redemption = components["schemas"]["Redemption"];
type Project = components["schemas"]["Project"];
type Addresses = components["schemas"]["Addresses"];
type RoleHolders = components["schemas"]["RoleHolders"];

const STATUS_FILTERS = [
  "",
  "Pending",
  "Funded",
  "Completed",
  "Rejected",
  "Cancelled",
] as const;

/**
 * Redemptions: pending/funded/completed requests, beneficiary compliance,
 * snapshotted quote, timeout, funding/rejection (broadcast from the connected
 * wallet), and claim status.
 */
export function Redemptions() {
  const [statusFilter, setStatusFilter] =
    useState<(typeof STATUS_FILTERS)[number]>("");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const redemptions = usePaginatedList<Redemption>(
    (cursor, signal) =>
      api.listRedemptions({
        signal,
        cursor,
        status: statusFilter || undefined,
      }),
    [statusFilter],
  );
  const project = useAsync<Project>((signal) => api.getProject({ signal }), []);
  const proj = project.status === "success" ? project.data : undefined;
  const chainId = proj?.chainId ?? 0;
  const finalityConfirmations = proj?.finalityConfirmations;
  // RWA amount scales by Project.decimals; the snapshotted quote is a
  // quote-token amount, whose decimals aren't in the HTTP contract, so they're
  // read on-chain best-effort for DISPLAY (falls back to grouped raw integer
  // when no wallet/RPC is available).
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
        <h1>Redemptions</h1>
        <p>
          Track requests through funding and claim; never guaranteed until
          claimed.
        </p>
      </header>

      <section className="card">
        <div className="field field--narrow">
          <label htmlFor="status-filter">Filter by status</label>
          <select
            id="status-filter"
            value={statusFilter}
            onChange={(e) =>
              setStatusFilter(e.target.value as (typeof STATUS_FILTERS)[number])
            }
          >
            {STATUS_FILTERS.map((s) => (
              <option key={s} value={s}>
                {s || "All"}
              </option>
            ))}
          </select>
        </div>

        <AsyncSection
          state={redemptions}
          onRetry={redemptions.reload}
          empty={(d) => d.length === 0}
          emptyLabel="No redemption requests match this filter."
        >
          {(data) => (
            <>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>ID</th>
                      <th>Beneficiary</th>
                      <th>RWA amount</th>
                      <th>Quote amount</th>
                      <th>Status</th>
                      <th>Beneficiary allowed</th>
                      <th>Timeout</th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.map((r) => (
                      <tr key={r.id}>
                        <td className="mono">{r.id}</td>
                        <td className="mono" title={r.beneficiary}>
                          {shortenAddress(r.beneficiary)}
                        </td>
                        <td>
                          {formatTokenAmount(r.rwaAmount ?? "", rwaDecimals)}
                        </td>
                        <td>
                          {formatTokenAmount(
                            r.quoteAmount ?? "",
                            quoteDecimals,
                          )}
                        </td>
                        <td>
                          <span className="badge-row">
                            <RedemptionStatusBadge
                              status={r.status}
                              claimable={r.claimable}
                              confirmations={r.confirmations}
                              finalityConfirmations={finalityConfirmations}
                            />
                            <RedemptionComplianceBadge
                              status={r.status}
                              beneficiaryAllowed={r.beneficiaryAllowed}
                            />
                          </span>
                        </td>
                        <td>{r.beneficiaryAllowed ? "Yes" : "No"}</td>
                        <td>{formatUnixSeconds(r.timeoutAt)}</td>
                        <td>
                          <button
                            type="button"
                            className="button button--secondary"
                            onClick={() => setSelectedId(r.id ?? null)}
                          >
                            Manage
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <PaginationFooter
                loadedCount={data.length}
                totalCount={redemptions.totalCount}
                hasMore={redemptions.hasMore}
                loadingMore={redemptions.loadingMore}
                loadMoreError={redemptions.loadMoreError}
                onLoadMore={redemptions.loadMore}
              />
            </>
          )}
        </AsyncSection>
      </section>

      {selectedId && (
        <RedemptionDetail
          id={selectedId}
          chainId={chainId}
          finalityConfirmations={finalityConfirmations}
          quoteDecimals={quoteDecimals}
          addresses={proj?.addresses}
          roles={proj?.roles}
          onChanged={redemptions.reload}
          onClose={() => setSelectedId(null)}
        />
      )}
    </div>
  );
}

function RedemptionDetail({
  id,
  chainId,
  finalityConfirmations,
  quoteDecimals,
  addresses,
  roles,
  onChanged,
  onClose,
}: {
  id: string;
  chainId: number;
  finalityConfirmations: number | undefined;
  quoteDecimals: number | undefined;
  addresses: Addresses | undefined;
  roles: RoleHolders | undefined;
  onChanged: () => void;
  onClose: () => void;
}) {
  const from = useWalletContext().address;
  const detail = useAsync<Redemption>(() => api.getRedemption(id), [id]);
  const [reasonCode, setReasonCode] = useState("");
  const [busy, setBusy] = useState<"fund" | "reject" | null>(null);
  const [progress, setProgress] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const blocked =
    detail.status === "success" &&
    redemptionBlockedByCompliance(
      detail.data.status,
      detail.data.beneficiaryAllowed,
    );

  function begin() {
    setError(null);
    setDone(null);
    setProgress(null);
  }

  // Funding pulls the recorded quoteAmount from the treasurer via
  // safeTransferFrom, so the escrow must be approved for that amount of the
  // quote token first: read the current allowance, approve + wait only when
  // it's short, then fundRedemption + wait. Blocked (beneficiary not Allowed)
  // funding stays blocked — it would revert on-chain.
  async function handleFund() {
    begin();
    if (blocked) return;
    if (!from) {
      setError("Wallet not connected.");
      return;
    }
    const escrow = addresses?.redemptionEscrow;
    const quoteToken = addresses?.quoteToken;
    const quoteAmount =
      detail.status === "success" ? detail.data.quoteAmount : undefined;
    if (!escrow || !quoteToken || !quoteAmount) {
      setError(
        "Missing escrow / quote token address or quote amount — project not deployed?",
      );
      return;
    }
    setBusy("fund");
    try {
      const amount = BigInt(quoteAmount);
      const allowance = await readErc20Allowance(
        chainId,
        quoteToken as Address,
        from,
        escrow as Address,
      );
      if (allowance < amount) {
        setProgress("Approving quote token…");
        const approveHash = await sendErc20Approve(
          chainId,
          from,
          quoteToken as Address,
          escrow as Address,
          amount,
        );
        await waitForTxReceipt(approveHash);
      }
      setProgress("Funding redemption…");
      const fundHash = await sendFundRedemption(
        chainId,
        from,
        escrow as Address,
        BigInt(id),
      );
      await waitForTxReceipt(fundHash);
      setDone("Redemption funded.");
      detail.reload();
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Funding failed.");
    } finally {
      setBusy(null);
      setProgress(null);
    }
  }

  async function handleReject() {
    begin();
    if (!from) {
      setError("Wallet not connected.");
      return;
    }
    const escrow = addresses?.redemptionEscrow;
    if (!escrow) {
      setError("Missing escrow address — project not deployed?");
      return;
    }
    let encoded: Hex;
    try {
      encoded = encodeReasonCode(reasonCode);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Invalid reason code.");
      return;
    }
    setBusy("reject");
    try {
      setProgress("Rejecting redemption…");
      const hash = await sendRejectRedemption(
        chainId,
        from,
        escrow as Address,
        BigInt(id),
        encoded,
      );
      await waitForTxReceipt(hash);
      setDone("Redemption rejected.");
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
      <h2>Redemption {id}</h2>
      <AsyncSection state={detail} onRetry={detail.reload}>
        {(r) => (
          <dl className="tx-preview__grid">
            <div className="tx-preview__row">
              <dt>Status</dt>
              <dd>
                <span className="badge-row">
                  <RedemptionStatusBadge
                    status={r.status}
                    claimable={r.claimable}
                    confirmations={r.confirmations}
                    finalityConfirmations={finalityConfirmations}
                  />
                  <RedemptionComplianceBadge
                    status={r.status}
                    beneficiaryAllowed={r.beneficiaryAllowed}
                  />
                </span>
              </dd>
            </div>
            <div className="tx-preview__row">
              <dt>Snapshotted quote</dt>
              <dd>{formatTokenAmount(r.quoteAmount ?? "", quoteDecimals)}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Created</dt>
              <dd>{formatUnixSeconds(r.createdAt)}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Timeout</dt>
              <dd>{formatUnixSeconds(r.timeoutAt)}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Confirmations</dt>
              <dd>{r.confirmations ?? 0}</dd>
            </div>
          </dl>
        )}
      </AsyncSection>

      <div className="tx-preview__actions u-mt-4">
        {/* Funding is a TREASURER_ROLE action; rejection is REDEMPTION_MANAGER_ROLE.
            Each is gated on the connected wallet holding that role, and the call
            is broadcast directly from that wallet (the contract's onlyRole check
            is the real authorization). */}
        <RoleGate roles={roles} role={ROLES.treasurer} action="fund redemptions">
          <button
            type="button"
            className="button button--primary"
            onClick={handleFund}
            disabled={busy !== null || blocked}
            title={
              blocked
                ? "The recorded beneficiary is not currently Allowed. Funding will revert until compliance status is restored."
                : undefined
            }
          >
            {busy === "fund" ? "Submitting…" : "Fund redemption"}
          </button>
        </RoleGate>
        <RoleGate
          roles={roles}
          role={ROLES.redemptionManager}
          action="reject redemptions"
        >
          <div className="field field--narrow">
            <label htmlFor="reasonCode">Rejection reason code</label>
            <input
              id="reasonCode"
              value={reasonCode}
              onChange={(e) => setReasonCode(e.target.value)}
            />
          </div>
          <button
            type="button"
            className="button button--danger"
            onClick={handleReject}
            disabled={busy !== null || !reasonCode}
          >
            {busy === "reject" ? "Submitting…" : "Reject redemption"}
          </button>
        </RoleGate>
        <button
          type="button"
          className="button button--secondary"
          onClick={onClose}
        >
          Close
        </button>
      </div>

      {blocked && (
        <p className="async-state--error" role="alert">
          Blocked by compliance — the recorded beneficiary is not currently
          Allowed, so funding will revert until compliance status is restored.
        </p>
      )}

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
