import { useState, type ChangeEvent } from "react";
import type { Hex } from "viem";
import { AsyncSection } from "../../components/AsyncSection";
import { PaginationFooter } from "../../components/PaginationFooter";
import { TableWrap } from "../../components/TableWrap";
import { useWalletContext } from "../../context/walletContextValue";
import { usePaginatedList } from "../../hooks/usePaginatedList";
import { parseBurnAmount, verifyBurnSignedResult } from "../../lib/burn";
import type { components } from "../../lib/api-types";
import { api, ApiError } from "../../lib/client";
import { formatIsoTimestamp, formatTokenAmount } from "../../lib/format";
import { parseSignedResult } from "../../lib/signedResult";
import {
  connectWallet,
  describeWalletError,
  sendBurn,
  waitForTxReceipt,
  type BurnAttestationInput,
} from "../../lib/wallet";

type Burn = components["schemas"]["Burn"];
type AssetRecord = components["schemas"]["AssetRecord"];
type Project = components["schemas"]["Project"];
type SignedResult = components["schemas"]["SignedResult"];

type BurnList = ReturnType<typeof usePaginatedList<Burn>>;

const shortId = (id: string | undefined) => (id ? id.slice(0, 8) : "-");

function saveBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  try {
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    document.body.appendChild(link);
    link.click();
    link.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}

/** Burn form for a Minted record, using the listed remaining amount when available */
export function BurnForm({
  record,
  rwaDecimals,
  remaining,
  onCreated,
}: {
  record: AssetRecord;
  rwaDecimals: number | undefined;
  remaining: string | undefined;
  onCreated: () => void;
}) {
  const [amount, setAmount] = useState("");
  const [proofsJson, setProofsJson] = useState("[]");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const id = `burn-${record.recordId}`;
  const limit = remaining ?? record.amount;

  async function handleCreate() {
    setError(null);
    if (rwaDecimals === undefined || !record.recordId) {
      setError(
        "Record ID or token decimals are unavailable. Try again shortly.",
      );
      return;
    }
    let proofs: unknown;
    try {
      proofs = JSON.parse(proofsJson);
    } catch {
      setError("Proofs must be valid JSON.");
      return;
    }
    let minimal: string;
    try {
      minimal = parseBurnAmount(amount, rwaDecimals, remaining);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Invalid amount.");
      return;
    }
    setSubmitting(true);
    try {
      await api.createBurn(
        {
          recordId: record.recordId,
          amount: minimal,
          proofs: proofs as Record<string, never>[],
        },
        `burn-${record.recordId}-${crypto.randomUUID()}`,
      );
      setAmount("");
      onCreated();
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Failed to create the burn.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div>
      <p className="field__hint">
        Burns permanently retire tokens from the Vault, which must hold the full
        amount.
      </p>
      <div className="field">
        <label htmlFor={`${id}-amount`}>
          Burn amount (whole units) for {record.recordId}
        </label>
        <input
          id={`${id}-amount`}
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
          aria-describedby={`${id}-amount-hint`}
        />
        <span className="field__hint" id={`${id}-amount-hint`}>
          {remaining !== undefined ? "Remaining to burn" : "Record amount"}:{" "}
          {formatTokenAmount(limit ?? "", rwaDecimals)}. Converted using the
          project&apos;s token decimals.
        </span>
      </div>
      <div className="field">
        <label htmlFor={`${id}-proofs`}>
          Proof hashes (JSON array) for {record.recordId}
        </label>
        <textarea
          id={`${id}-proofs`}
          rows={3}
          className="mono"
          value={proofsJson}
          onChange={(e) => setProofsJson(e.target.value)}
        />
        <span className="field__hint">
          Optional evidence: a JSON array of{" "}
          <code className="mono">{"{ type, sha256, uri? }"}</code> entries. Use{" "}
          <code className="mono">[]</code> for none.
        </span>
      </div>
      <button
        type="button"
        className="button button--primary"
        onClick={handleCreate}
        disabled={submitting || !amount}
      >
        {submitting ? "Creating…" : "Create burn"}
      </button>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

/** Burn list with package download, reissue, upload and wallet confirmation */
export function BurnsSection({
  burns,
  project,
  rwaDecimals,
  onBurned,
}: {
  burns: BurnList;
  project: Project | undefined;
  rwaDecimals: number | undefined;
  onBurned: () => void;
}) {
  const wallet = useWalletContext();
  const [error, setError] = useState<string | null>(null);
  const [txHash, setTxHash] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  // Matching auditor and digest fields, awaiting confirmation
  const [ready, setReady] = useState<{
    burn: Burn;
    attestation: BurnAttestationInput;
    signed: SignedResult;
  } | null>(null);

  async function handleFile(burn: Burn, e: ChangeEvent<HTMLInputElement>) {
    setError(null);
    setTxHash(null);
    setReady(null);
    const file = e.target.files?.[0];
    if (!file) return;
    try {
      if (!project)
        throw new Error("Project still loading. Try again shortly.");
      const signed = parseSignedResult(await file.text());
      const attestation = verifyBurnSignedResult(signed, burn, project);
      setReady({ burn, attestation, signed });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not read the file.");
    }
  }

  async function handleSend() {
    if (!ready || !project) return;
    const supplyController = project.addresses?.supplyController;
    if (!supplyController || project.chainId === undefined) return;
    setError(null);
    let from = wallet.address;
    if (!from) {
      try {
        from = (await connectWallet()).address;
      } catch {
        setError("Connect a wallet on the project chain to burn.");
        return;
      }
    }
    setSending(true);
    try {
      const hash = await sendBurn(
        project.chainId,
        from,
        supplyController as `0x${string}`,
        ready.attestation,
        ready.signed.signature as Hex,
      );
      await waitForTxReceipt(hash);
      setTxHash(hash);
      setReady(null);
      // Refresh the list; Burned may not be indexed yet
      onBurned();
    } catch (err) {
      setError(describeWalletError(err));
    } finally {
      setSending(false);
    }
  }

  return (
    <div>
      <AsyncSection
        state={burns}
        onRetry={burns.reload}
        empty={(d) => d.length === 0}
        emptyLabel="No burns yet."
      >
        {(data) => (
          <>
            <TableWrap label="Burns">
              <table>
                <thead>
                  <tr>
                    <th>Burn ID</th>
                    <th>Record</th>
                    <th>Amount</th>
                    <th>Status</th>
                    <th>Valid until</th>
                    <th>Transaction</th>
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {data.map((b) => (
                    <BurnRow
                      key={b.burnId}
                      burn={b}
                      rwaDecimals={rwaDecimals}
                      onReissued={burns.reload}
                      onFile={(e) => handleFile(b, e)}
                    />
                  ))}
                </tbody>
              </table>
            </TableWrap>
            <PaginationFooter
              loadedCount={data.length}
              totalCount={burns.totalCount}
              hasMore={burns.hasMore}
              loadingMore={burns.loadingMore}
              loadMoreError={burns.loadMoreError}
              onLoadMore={burns.loadMore}
            />
          </>
        )}
      </AsyncSection>

      {ready && (
        <div role="status">
          <h3>Confirm burn</h3>
          <dl className="tx-preview__grid">
            <div className="tx-preview__row">
              <dt>Record</dt>
              <dd>{ready.burn.recordId}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Amount</dt>
              <dd>
                {formatTokenAmount(ready.burn.amount ?? "", rwaDecimals)} (
                {ready.burn.amount} minimal units)
              </dd>
            </div>
            <div className="tx-preview__row">
              <dt>Operation ID</dt>
              <dd className="mono">{ready.burn.operationId}</dd>
            </div>
          </dl>
          <p className="field__hint">
            The result&apos;s auditor and digest fields match this burn. Confirm
            to permanently burn Vault inventory; the contract checks the
            signature and available balance.
          </p>
          <button
            type="button"
            className="button button--primary"
            onClick={handleSend}
            disabled={sending}
          >
            {sending ? "Burning…" : "Confirm and burn"}
          </button>{" "}
          <button
            type="button"
            className="button button--secondary"
            onClick={() => setReady(null)}
            disabled={sending}
          >
            Cancel
          </button>
        </div>
      )}
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {txHash && (
        <p role="status">
          Burn broadcast. Transaction: <span className="mono">{txHash}</span>.
          The server marks it Burned after indexing the event.
        </p>
      )}
    </div>
  );
}

function BurnRow({
  burn,
  rwaDecimals,
  onReissued,
  onFile,
}: {
  burn: Burn;
  rwaDecimals: number | undefined;
  onReissued: () => void;
  onFile: (e: ChangeEvent<HTMLInputElement>) => void;
}) {
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const pending = burn.status === "Pending";
  const expired =
    pending &&
    burn.validUntil !== undefined &&
    burn.validUntil * 1000 <= Date.now();
  const label = shortId(burn.burnId);

  async function run(action: () => Promise<void>, failure: string) {
    if (!burn.burnId) return;
    setError(null);
    setBusy(true);
    try {
      await action();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : failure);
    } finally {
      setBusy(false);
    }
  }

  return (
    <tr>
      <td className="mono" title={burn.burnId}>
        {label}
      </td>
      <td className="mono">{burn.recordId}</td>
      <td>{formatTokenAmount(burn.amount ?? "", rwaDecimals)}</td>
      <td>
        <span
          className={`badge ${burn.status === "Burned" ? "badge--success" : "badge--info"}`}
        >
          {burn.status}
        </span>
        {expired && <span className="field__hint"> expired</span>}
      </td>
      <td>
        {burn.validUntil
          ? formatIsoTimestamp(new Date(burn.validUntil * 1000).toISOString())
          : "-"}
      </td>
      <td className="mono">{burn.burnTxHash ?? "-"}</td>
      <td>
        <button
          type="button"
          className="button button--secondary"
          disabled={busy}
          aria-label={`Download package for burn ${label}`}
          onClick={() =>
            run(async () => {
              saveBlob(
                await api.downloadBurnPackage(burn.burnId!),
                `${burn.burnId}.rwa`,
              );
            }, "Download failed.")
          }
        >
          Download package
        </button>
        {expired && (
          <button
            type="button"
            className="button button--secondary"
            disabled={busy}
            aria-label={`Reissue burn ${label}`}
            onClick={() =>
              run(async () => {
                await api.reissueBurn(
                  burn.burnId!,
                  `reissue-${burn.burnId}-${burn.validUntil}`,
                );
                onReissued();
              }, "Reissue failed.")
            }
          >
            Reissue
          </button>
        )}
        {pending && !expired && (
          <div className="field">
            <label htmlFor={`burn-file-${burn.burnId}`}>
              Upload signed result for burn {label}
            </label>
            <input
              id={`burn-file-${burn.burnId}`}
              type="file"
              accept="application/json,.json"
              onChange={onFile}
            />
          </div>
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
