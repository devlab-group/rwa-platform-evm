import { useState } from "react";
import { AsyncSection } from "../../components/AsyncSection";
import { ConfirmStep } from "../../components/ConfirmStep";
import { PaginationFooter } from "../../components/PaginationFooter";
import { usePaginatedList } from "../../hooks/usePaginatedList";
import { api, ApiError } from "../../lib/client";
import type { components } from "../../lib/api-types";
import { formatUnixSeconds, shortenAddress } from "../../lib/format";

type WalletStatus = components["schemas"]["WalletStatus"];
type Challenge = components["schemas"]["Challenge"];
type WebhookEvent = components["schemas"]["WebhookEvent"];
type AuditLog = components["schemas"]["AuditLog"];
type ComplianceStatus = NonNullable<WalletStatus["status"]>;
type WebhookOutcome = NonNullable<WebhookEvent["outcome"]>;

const STATUS_TONE: Record<ComplianceStatus, string> = {
  Unknown: "badge--neutral",
  Allowed: "badge--success",
  Blocked: "badge--danger",
};

const WEBHOOK_TONE: Record<WebhookOutcome, string> = {
  Allowed: "badge--success",
  Blocked: "badge--danger",
  Pending: "badge--neutral",
};

/**
 * Compliance: investor status/expiry, manual allow/block, wallet-ownership
 * challenge/evidence, KYC webhook history, and audit log.
 */
export function Compliance() {
  const wallets = usePaginatedList<WalletStatus>(
    (cursor, signal) => api.listWallets({ signal, cursor }),
    [],
  );
  const webhooks = usePaginatedList<WebhookEvent>(
    (cursor, signal) => api.listWebhookEvents({ signal, cursor }),
    [],
  );
  const auditLogs = usePaginatedList<AuditLog>(
    (cursor, signal) => api.listAuditLogs({ signal, cursor }),
    [],
  );

  return (
    <div>
      <header className="app-main__header">
        <h1>Compliance</h1>
        <p>Investor allowlist status, expiry, and wallet-ownership evidence.</p>
      </header>

      <section className="card">
        <h2>Wallets</h2>
        <AsyncSection
          state={wallets}
          onRetry={wallets.reload}
          empty={(d) => d.length === 0}
          emptyLabel="No wallets recorded yet."
        >
          {(data) => (
            <>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Address</th>
                      <th>Status</th>
                      <th>Valid until</th>
                      <th>Ownership verified</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.map((w) => (
                      <tr key={w.address}>
                        <td className="mono" title={w.address}>
                          {shortenAddress(w.address)}
                        </td>
                        <td>
                          {w.status && (
                            <span className={`badge ${STATUS_TONE[w.status]}`}>
                              {w.status}
                            </span>
                          )}
                        </td>
                        <td>{formatUnixSeconds(w.validUntil)}</td>
                        <td>{w.ownershipVerified ? "Yes" : "No"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <PaginationFooter
                loadedCount={data.length}
                totalCount={wallets.totalCount}
                hasMore={wallets.hasMore}
                loadingMore={wallets.loadingMore}
                loadMoreError={wallets.loadMoreError}
                onLoadMore={wallets.loadMore}
              />
            </>
          )}
        </AsyncSection>
      </section>

      <section className="card">
        <h2>Manual allow / block</h2>
        <SetStatusForm onSubmitted={wallets.reload} />
      </section>

      <section className="card">
        <h2>Wallet-ownership challenge</h2>
        <ChallengeGenerator />
      </section>

      <section className="card">
        <h2>Webhook history</h2>
        <AsyncSection
          state={webhooks}
          onRetry={webhooks.reload}
          empty={(d) => d.length === 0}
          emptyLabel="No KYC webhook deliveries recorded yet."
        >
          {(data) => (
            <>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Address</th>
                      <th>Provider</th>
                      <th>Outcome</th>
                      <th>Applied</th>
                      <th>Received</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.map((w) => (
                      <tr key={w.id}>
                        <td className="mono" title={w.address}>
                          {shortenAddress(w.address)}
                        </td>
                        <td>{w.provider ?? "—"}</td>
                        <td>
                          {w.outcome && (
                            <span
                              className={`badge ${WEBHOOK_TONE[w.outcome]}`}
                            >
                              {w.outcome}
                            </span>
                          )}
                        </td>
                        <td>{w.applied ? "Yes" : "No"}</td>
                        <td>{w.receivedAt ?? "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <PaginationFooter
                loadedCount={data.length}
                totalCount={webhooks.totalCount}
                hasMore={webhooks.hasMore}
                loadingMore={webhooks.loadingMore}
                loadMoreError={webhooks.loadMoreError}
                onLoadMore={webhooks.loadMore}
              />
            </>
          )}
        </AsyncSection>
      </section>

      <section className="card">
        <h2>Audit log</h2>
        <AsyncSection
          state={auditLogs}
          onRetry={auditLogs.reload}
          empty={(d) => d.length === 0}
          emptyLabel="No audit log entries recorded yet."
        >
          {(data) => (
            <>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Category</th>
                      <th>Actor</th>
                      <th>Action</th>
                      <th>Target</th>
                      <th>When</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.map((entry) => (
                      <tr key={entry.id}>
                        <td>{entry.category ?? "—"}</td>
                        <td className="mono" title={entry.actor}>
                          {shortenAddress(entry.actor)}
                        </td>
                        <td>{entry.action ?? "—"}</td>
                        <td className="mono" title={entry.target}>
                          {entry.target ? shortenAddress(entry.target) : "—"}
                        </td>
                        <td>{entry.createdAt ?? "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <PaginationFooter
                loadedCount={data.length}
                totalCount={auditLogs.totalCount}
                hasMore={auditLogs.hasMore}
                loadingMore={auditLogs.loadingMore}
                loadMoreError={auditLogs.loadMoreError}
                onLoadMore={auditLogs.loadMore}
              />
            </>
          )}
        </AsyncSection>
      </section>
    </div>
  );
}

function SetStatusForm({ onSubmitted }: { onSubmitted: () => void }) {
  const [address, setAddress] = useState("");
  const [status, setStatus] = useState<ComplianceStatus>("Allowed");
  const [validUntil, setValidUntil] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<string | null>(null);
  const [confirmGateKey, setConfirmGateKey] = useState(0);

  async function handleSubmit() {
    setSubmitting(true);
    setError(null);
    setResult(null);
    try {
      const ref = await api.setComplianceStatus(
        {
          address,
          status,
          validUntil: validUntil ? Number(validUntil) : undefined,
        },
        `compliance-${address}-${status}`,
      );
      setResult(ref.txHash ?? "submitted");
      setConfirmGateKey((k) => k + 1); // collapse the confirm panel back down
      onSubmitted();
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Failed to update status.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div>
      <div className="field">
        <label htmlFor="wallet-address">Address</label>
        <input
          id="wallet-address"
          className="mono"
          value={address}
          onChange={(e) => setAddress(e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="wallet-status">Status</label>
        <select
          id="wallet-status"
          value={status}
          onChange={(e) => setStatus(e.target.value as ComplianceStatus)}
        >
          <option value="Allowed">Allowed</option>
          <option value="Blocked">Blocked</option>
          <option value="Unknown">Unknown</option>
        </select>
      </div>
      <div className="field">
        <label htmlFor="valid-until">
          Valid until (unix seconds, optional)
        </label>
        <input
          id="valid-until"
          value={validUntil}
          onChange={(e) => setValidUntil(e.target.value)}
        />
      </div>
      <ConfirmStep
        key={confirmGateKey}
        reviewLabel="Review status change"
        confirmLabel="Confirm set status"
        confirmHeading="Confirm compliance status change"
        confirmWarning="This calls the server's compliance hot key directly and takes effect immediately — no wallet signature step follows to catch a mistake."
        disabled={!address}
        submitting={submitting}
        onConfirm={handleSubmit}
      >
        <dl className="tx-preview__grid">
          <div className="tx-preview__row">
            <dt>Address</dt>
            <dd className="mono">{address}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>New status</dt>
            <dd>{status}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Valid until</dt>
            <dd>{validUntil || "(no expiry set)"}</dd>
          </div>
        </dl>
      </ConfirmStep>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {result && (
        <p role="status">
          Submitted. Reference: <span className="mono">{result}</span>
        </p>
      )}
    </div>
  );
}

function ChallengeGenerator() {
  const [address, setAddress] = useState("");
  const [challenge, setChallenge] = useState<Challenge | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleGenerate() {
    setSubmitting(true);
    setError(null);
    try {
      setChallenge(await api.createChallenge({ address }));
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Failed to create challenge.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div>
      <div className="field">
        <label htmlFor="challenge-address">Address</label>
        <input
          id="challenge-address"
          className="mono"
          value={address}
          onChange={(e) => setAddress(e.target.value)}
        />
      </div>
      <button
        type="button"
        className="button button--secondary"
        onClick={handleGenerate}
        disabled={submitting || !address}
      >
        {submitting ? "Generating…" : "Generate challenge"}
      </button>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {challenge && (
        <dl className="tx-preview__grid u-mt-4">
          <div className="tx-preview__row">
            <dt>Nonce</dt>
            <dd>{challenge.nonce}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Message to sign</dt>
            <dd>{challenge.message}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Expires</dt>
            <dd>{challenge.expiresAt}</dd>
          </div>
        </dl>
      )}
    </div>
  );
}
