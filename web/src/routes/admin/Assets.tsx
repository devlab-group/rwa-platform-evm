import { useEffect, useMemo, useState, type ChangeEvent } from "react";
import type { Address, Hex } from "viem";
import { AsyncSection } from "../../components/AsyncSection";
import { PaginationFooter } from "../../components/PaginationFooter";
import { useAsync } from "../../hooks/useAsync";
import { usePaginatedList } from "../../hooks/usePaginatedList";
import { api, ApiError } from "../../lib/client";
import type { components } from "../../lib/api-types";
import { useWalletContext } from "../../context/walletContextValue";
import {
  formatIsoTimestamp,
  formatTokenAmount,
  toMinimalUnits,
} from "../../lib/format";
import { buildMetadataSkeleton } from "../../lib/jsonSchemaSkeleton";
import { buildSignerPolicy, type SignerPolicy } from "../../lib/signerPolicy";
import {
  connectWallet,
  sendMint,
  waitForTxReceipt,
  type MintAttestationInput,
} from "../../lib/wallet";
import { TableWrap } from "../../components/TableWrap";

type AssetRecord = components["schemas"]["AssetRecord"];
type SignedResult = components["schemas"]["SignedResult"];
type Project = components["schemas"]["Project"];
type StoredProfile = components["schemas"]["StoredProfile"];

// Empty metadata when no profile schema is available
const EMPTY_METADATA = "{\n  \n}";

const RECORD_TONE: Record<NonNullable<AssetRecord["status"]>, string> = {
  Draft: "badge--neutral",
  Pending: "badge--info",
  Signed: "badge--info",
  Minted: "badge--success",
  Rejected: "badge--danger",
};

/**
 * Creates records/packages and submits signed mints; project-specific metadata/proofs use JSON
 * editors
 */
export function Assets() {
  const records = usePaginatedList<AssetRecord>(
    (cursor, signal) => api.listRecords({ signal, cursor }),
    [],
  );
  // Use project decimals for displayed whole units and signed minimal units
  const project = useAsync<Project>((signal) => api.getProject({ signal }), []);
  const rwaDecimals =
    project.status === "success" ? project.data.decimals : undefined;

  // Seed metadata from the profile schema, falling back to an empty object
  const profile = useAsync<StoredProfile | null>(
    (signal) => loadProfile(signal),
    [],
  );
  const metadataSkeleton = useMemo(() => {
    if (profile.status !== "success" || !profile.data) return undefined;
    const raw = profile.data.profile as Record<string, unknown> | undefined;
    return buildMetadataSkeleton(raw?.assetSchema);
  }, [profile.status, profile.data]);
  const recordIdLabel =
    (profile.status === "success" &&
      profile.data &&
      (profile.data.profile as Record<string, unknown>)?.recordIdLabel) ||
    undefined;

  // Pin policy to the profile UUID, separate from its digest
  const projectUuid =
    profile.status === "success" &&
    profile.data &&
    typeof (profile.data.profile as Record<string, unknown>)?.projectId ===
      "string"
      ? ((profile.data.profile as Record<string, unknown>).projectId as string)
      : undefined;

  return (
    <div>
      <header className="app-main__header">
        <h1>Assets</h1>
        <p>
          Create records, download audit packages and mint with auditor
          signatures.
        </p>
      </header>

      <section className="card">
        <h2>Create record</h2>
        <CreateRecordForm
          onCreated={records.reload}
          rwaDecimals={rwaDecimals}
          metadataSkeleton={metadataSkeleton}
          recordIdLabel={
            typeof recordIdLabel === "string" ? recordIdLabel : undefined
          }
        />
      </section>

      <section className="card">
        <h2>Signer policy (offline signing trust root)</h2>
        <p className="field__hint">
          This is the <code className="mono">--policy policy.json</code> file
          the auditor needs for <code className="mono">signer sign</code>. It
          restricts signing to the specified chain, contracts, auditor and
          project. Give it to the auditor with the .rwa package; the signer
          rejects mismatches.
        </p>
        <SignerPolicySection
          project={project.status === "success" ? project.data : undefined}
          projectUuid={projectUuid}
        />
      </section>

      <section className="card">
        <h2>Records</h2>
        <AsyncSection
          state={records}
          onRetry={records.reload}
          empty={(d) => d.length === 0}
          emptyLabel="No asset records yet."
        >
          {(data) => (
            <>
              <TableWrap label="Records">
                <table>
                  <thead>
                    <tr>
                      <th>Record ID</th>
                      <th>Status</th>
                      <th>Amount</th>
                      <th>Metadata digest</th>
                      <th>IPFS CID</th>
                      <th>Created</th>
                      <th>Package</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.map((r) => (
                      <RecordRow
                        key={r.recordId}
                        record={r}
                        rwaDecimals={rwaDecimals}
                      />
                    ))}
                  </tbody>
                </table>
              </TableWrap>
              <PaginationFooter
                loadedCount={data.length}
                totalCount={records.totalCount}
                hasMore={records.hasMore}
                loadingMore={records.loadingMore}
                loadMoreError={records.loadMoreError}
                onLoadMore={records.loadMore}
              />
            </>
          )}
        </AsyncSection>
      </section>

      <section className="card">
        <h2>Auditor signature upload &amp; mint</h2>
        <p className="field__hint">
          Upload the auditor signature to submit SupplyController.mint from your
          wallet. The server updates the record after observing Minted.
        </p>
        <SignatureUploadForm
          project={project.status === "success" ? project.data : undefined}
          records={records.status === "success" ? records.data : []}
          onMinted={records.reload}
        />
      </section>
    </div>
  );
}

// Download the public deployment policy without a wallet
function downloadSignerPolicy(policy: SignerPolicy): void {
  const blob = new Blob([JSON.stringify(policy, null, 2)], {
    type: "application/json",
  });
  const url = URL.createObjectURL(blob);
  try {
    const link = document.createElement("a");
    link.href = url;
    link.download = "policy.json";
    document.body.appendChild(link);
    link.click();
    link.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}

function SignerPolicySection({
  project,
  projectUuid,
}: {
  project: Project | undefined;
  projectUuid: string | undefined;
}) {
  const policy = buildSignerPolicy(project, projectUuid);

  if (!policy) {
    return (
      <p className="field__hint">
        Create the Asset Profile and deploy first. The policy needs contract
        addresses, auditor, profile digest and project ID.
      </p>
    );
  }

  const rows: [string, string][] = [
    ["Chain ID", policy.chainId],
    ["SupplyController (controller)", policy.controller],
    ["Vault", policy.vault],
    ["Auditor", policy.auditor],
    ["Project ID", policy.projectId],
    ["Profile digest", policy.profileDigest],
  ];

  return (
    <div>
      <dl className="tx-preview__grid">
        {rows.map(([label, value]) => (
          <div className="tx-preview__row" key={label}>
            <dt>{label}</dt>
            <dd className="mono">{value}</dd>
          </div>
        ))}
      </dl>
      <button
        type="button"
        className="button button--primary"
        onClick={() => downloadSignerPolicy(policy)}
      >
        Download policy.json
      </button>
    </div>
  );
}

// Treat the admin profile endpoint's 404 as an absent profile
async function loadProfile(signal: AbortSignal): Promise<StoredProfile | null> {
  try {
    return await api.getProfile({ signal });
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null;
    throw err;
  }
}

function RecordRow({
  record,
  rwaDecimals,
}: {
  record: AssetRecord;
  rwaDecimals: number | undefined;
}) {
  const [error, setError] = useState<string | null>(null);
  const [downloading, setDownloading] = useState(false);

  // Download the protected package with bearer authentication through a Blob URL
  async function handleDownload() {
    if (!record.recordId) return;
    setError(null);
    setDownloading(true);
    try {
      const blob = await api.downloadPackage(record.recordId);
      const url = URL.createObjectURL(blob);
      try {
        const link = document.createElement("a");
        link.href = url;
        link.download = `${record.recordId}.rwa`;
        document.body.appendChild(link);
        link.click();
        link.remove();
      } finally {
        URL.revokeObjectURL(url);
      }
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Download failed.");
    } finally {
      setDownloading(false);
    }
  }

  return (
    <tr>
      <td className="mono">{record.recordId}</td>
      <td>
        {record.status && (
          <span className={`badge ${RECORD_TONE[record.status]}`}>
            {record.status}
          </span>
        )}
      </td>
      <td>{formatTokenAmount(record.amount ?? "", rwaDecimals)}</td>
      <td className="mono">{record.metadataDigest ?? "-"}</td>
      <td className="mono">{record.cid ?? "-"}</td>
      <td>{formatIsoTimestamp(record.createdAt)}</td>
      <td>
        {record.recordId && (
          <>
            <button
              type="button"
              className="button button--secondary"
              onClick={handleDownload}
              disabled={downloading}
            >
              {downloading ? "Downloading…" : "Download .rwa"}
            </button>
            {error && (
              <p className="async-state--error" role="alert">
                {error}
              </p>
            )}
          </>
        )}
      </td>
    </tr>
  );
}

const PROOFS_EXAMPLE = `[
  {
    "type": "custody-attestation",
    "sha256": "3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b",
    "uri": "ipfs://bafybeih..."
  }
]`;

function CreateRecordForm({
  onCreated,
  rwaDecimals,
  metadataSkeleton,
  recordIdLabel,
}: {
  onCreated: () => void;
  rwaDecimals: number | undefined;
  // Schema-derived metadata template, undefined until a profile is available
  metadataSkeleton: string | undefined;
  // Profile label for the record identifier, such as "Serial number"
  recordIdLabel: string | undefined;
}) {
  const [recordId, setRecordId] = useState("");
  const [amount, setAmount] = useState("");
  const [assetJson, setAssetJson] = useState(
    metadataSkeleton ?? EMPTY_METADATA,
  );
  const [assetEdited, setAssetEdited] = useState(false);
  const [proofsJson, setProofsJson] = useState("[]");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  // Apply the loaded skeleton only before editing starts
  useEffect(() => {
    if (metadataSkeleton && !assetEdited) setAssetJson(metadataSkeleton);
  }, [metadataSkeleton, assetEdited]);

  async function handleCreate() {
    setError(null);
    if (rwaDecimals === undefined) {
      setError("Token decimals not available yet - project still loading.");
      return;
    }
    let asset: unknown;
    let proofs: unknown;
    try {
      asset = JSON.parse(assetJson);
      proofs = JSON.parse(proofsJson);
    } catch {
      setError("Asset metadata and proofs must be valid JSON.");
      return;
    }
    // Convert to record minimal units before auditor signing
    let minimalAmount: string;
    try {
      minimalAmount = toMinimalUnits(amount, rwaDecimals);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Invalid amount.");
      return;
    }
    setSubmitting(true);
    try {
      await api.createRecord(
        {
          recordId,
          asset: asset as Record<string, never>,
          amount: minimalAmount,
          proofs: proofs as Record<string, never>[],
        },
        `create-${recordId}`,
      );
      setRecordId("");
      setAmount("");
      onCreated();
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Failed to create record.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div>
      <div className="field">
        <label htmlFor="recordId">Record ID</label>
        <input
          id="recordId"
          value={recordId}
          onChange={(e) => setRecordId(e.target.value)}
          aria-describedby="recordId-hint"
        />
        <span className="field__hint" id="recordId-hint">
          A unique identifier for this asset record
          {recordIdLabel ? ` - the ${recordIdLabel.toLowerCase()}` : ""} (e.g.
          serial, deed or invoice number). Immutable after creation.
        </span>
      </div>
      <div className="field">
        <label htmlFor="amount">Amount (whole units)</label>
        <input
          id="amount"
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
          aria-describedby="amount-hint"
        />
        <span className="field__hint" id="amount-hint">
          Whole tokens represented (e.g. 100.5), converted to minimal units
          using the project&apos;s decimals.
        </span>
      </div>
      <div className="field">
        <label htmlFor="assetJson">Metadata (asset fields as JSON)</label>
        <textarea
          id="assetJson"
          rows={8}
          className="mono"
          value={assetJson}
          onChange={(e) => {
            setAssetJson(e.target.value);
            setAssetEdited(true);
          }}
          aria-describedby="assetJson-hint"
        />
        <span className="field__hint" id="assetJson-hint">
          Metadata matching the Asset Profile schema. Replace the placeholders;
          the server validates it.
        </span>
      </div>
      <div className="field">
        <label htmlFor="proofsJson">Proof hashes (JSON array)</label>
        <textarea
          id="proofsJson"
          rows={3}
          className="mono"
          value={proofsJson}
          onChange={(e) => setProofsJson(e.target.value)}
          aria-describedby="proofsJson-hint"
        />
        <span className="field__hint" id="proofsJson-hint">
          Optional evidence references (custody attestations, appraisals, audit
          certificates), as a JSON array of{" "}
          <code className="mono">{"{ type, sha256, uri? }"}</code> entries:{" "}
          <strong>type</strong> is your own label, <strong>sha256</strong> is
          the document&apos;s SHA-256: 64 lowercase hex characters (no{" "}
          <code className="mono">0x</code>), <strong>uri</strong> is an optional
          document location. Use <code className="mono">[]</code> for no
          evidence. Example:
        </span>
        <pre className="field__example mono">{PROOFS_EXAMPLE}</pre>
      </div>
      <button
        type="button"
        className="button button--primary"
        onClick={handleCreate}
        disabled={submitting || !recordId || !amount}
      >
        {submitting ? "Creating…" : "Create record"}
      </button>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

// Required fields from shared/schemas/signed-result.schema.json
const SIGNED_RESULT_FIELDS = [
  "formatVersion",
  "auditor",
  "primaryType",
  "typedDataDigest",
  "signature",
  "signedAt",
] as const;

// parseSignedResult requires non-empty string fields without checking signature validity
function parseSignedResult(text: string): SignedResult {
  let obj: unknown;
  try {
    obj = JSON.parse(text);
  } catch {
    throw new Error("The selected file is not valid JSON.");
  }
  if (typeof obj !== "object" || obj === null || Array.isArray(obj)) {
    throw new Error("signed-result.json must be a JSON object.");
  }
  const rec = obj as Record<string, unknown>;
  const missing = SIGNED_RESULT_FIELDS.filter(
    (k) => typeof rec[k] !== "string" || rec[k] === "",
  );
  if (missing.length > 0) {
    throw new Error(
      `Not a signed-result.json - missing field(s): ${missing.join(", ")}.`,
    );
  }
  return {
    formatVersion: String(rec.formatVersion),
    auditor: String(rec.auditor),
    primaryType: String(rec.primaryType),
    typedDataDigest: String(rec.typedDataDigest),
    signature: String(rec.signature),
    signedAt: String(rec.signedAt),
  };
}

function SignatureUploadForm({
  project,
  records,
  onMinted,
}: {
  project: Project | undefined;
  records: AssetRecord[];
  onMinted: () => void;
}) {
  const wallet = useWalletContext();
  const [recordId, setRecordId] = useState("");
  // Load the complete signer output to avoid transcription errors
  const [signed, setSigned] = useState<SignedResult | null>(null);
  const [fileName, setFileName] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [txHash, setTxHash] = useState<string | null>(null);

  async function handleFile(e: ChangeEvent<HTMLInputElement>) {
    setError(null);
    setTxHash(null);
    setSigned(null);
    setFileName(null);
    const file = e.target.files?.[0];
    if (!file) return;
    try {
      setSigned(parseSignedResult(await file.text()));
      setFileName(file.name);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not read the file.");
    }
  }

  async function handleMint() {
    if (!signed) return;
    setError(null);
    setTxHash(null);

    // Build the attestation from loaded record and project fields
    if (!project) {
      setError("Project not loaded yet - try again in a moment.");
      return;
    }
    const record = records.find((r) => r.recordId === recordId);
    if (!record) {
      setError(
        "No loaded record matches this ID. Check the records above or load another page.",
      );
      return;
    }
    const supplyController = project.addresses?.supplyController;
    const vault = project.addresses?.vault;
    if (!supplyController || !vault || project.chainId === undefined) {
      setError(
        "Deployment is incomplete: SupplyController, Vault or chain is missing.",
      );
      return;
    }
    if (
      record.recordKey === undefined ||
      record.nonce === undefined ||
      record.validUntil === undefined ||
      record.metadataDigest === undefined ||
      record.amount === undefined
    ) {
      setError(
        "Record attestation fields are missing: recordKey/nonce/validUntil/metadata/amount.",
      );
      return;
    }
    if (!project.auditor || !project.profileDigest) {
      setError("Project auditor or profile digest is missing.");
      return;
    }
    // Check auditor address before broadcast; the contract verifies EOA/Safe signatures
    if (signed.auditor.toLowerCase() !== project.auditor.toLowerCase()) {
      setError(
        `Signed by ${signed.auditor}, but this project's auditor is ${project.auditor}. Wrong signed-result.json.`,
      );
      return;
    }

    const attestation: MintAttestationInput = {
      auditor: project.auditor as Address,
      profileDigest: project.profileDigest as Hex,
      recordKey: record.recordKey as Hex,
      metadataDigest: record.metadataDigest as Hex,
      amount: BigInt(record.amount),
      nonce: BigInt(record.nonce),
      validUntil: BigInt(record.validUntil),
      vault: vault as Address,
    };

    // Connect a wallet if login has not already connected one
    let from = wallet.address;
    if (!from) {
      try {
        from = (await connectWallet()).address;
      } catch {
        setError("Connect your wallet on the project chain to mint.");
        return;
      }
    }

    setSubmitting(true);
    try {
      const hash = await sendMint(
        project.chainId,
        from,
        supplyController as Address,
        attestation,
        signed.signature as Hex,
      );
      await waitForTxReceipt(hash);
      setTxHash(hash);
      // Reload while the server projects Minted
      onMinted();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Mint transaction failed.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div>
      <div className="field">
        <label htmlFor="sigRecordId">Record ID</label>
        <input
          id="sigRecordId"
          value={recordId}
          onChange={(e) => setRecordId(e.target.value)}
          aria-describedby="sigRecordId-hint"
        />
        <span className="field__hint" id="sigRecordId-hint">
          Enter the ID used for the .rwa package; it must match a loaded record
          above. Its recordKey, nonce, validUntil, metadata digest and amount
          supply the attestation.
        </span>
      </div>
      <div className="field">
        <label htmlFor="signedResultFile">Signed result</label>
        <input
          id="signedResultFile"
          type="file"
          accept="application/json,.json"
          onChange={handleFile}
          aria-describedby="signedResultFile-hint"
        />
        <span className="field__hint" id="signedResultFile-hint">
          Upload the <code className="mono">signed-result.json</code> produced
          by the auditor from the .rwa package. For a Safe auditor, use the
          output of <code className="mono">signer combine</code> from
          owners&apos; shares. The console checks the project auditor address;
          the contract verifies the MintAttestation signature.
        </span>
      </div>

      {signed && (
        <div role="status">
          <dl className="tx-preview__grid">
            <div className="tx-preview__row">
              <dt>File</dt>
              <dd>{fileName}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Auditor</dt>
              <dd className="mono">{signed.auditor}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Attestation</dt>
              <dd>{signed.primaryType}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Signed at</dt>
              <dd>{signed.signedAt}</dd>
            </div>
          </dl>
        </div>
      )}

      <button
        type="button"
        className="button button--primary"
        onClick={handleMint}
        disabled={submitting || !recordId || !signed}
      >
        {submitting ? "Minting…" : "Upload signature & mint"}
      </button>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {txHash && (
        <p role="status">
          Mint broadcast. Transaction: <span className="mono">{txHash}</span>.
          The server marks the record Minted after observing the event.
        </p>
      )}
    </div>
  );
}
