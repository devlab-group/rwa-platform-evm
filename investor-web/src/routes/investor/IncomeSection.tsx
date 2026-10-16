import { useState } from "react";
import { AsyncSection } from "../../components/AsyncSection";
import {
  TransactionPreview,
  type ConfirmationState,
} from "../../components/TransactionPreview";
import { useAsync } from "../../hooks/useAsync";
import { api } from "../../lib/client";
import type { components } from "../../lib/api-types";
import { resolveQuoteDecimals } from "../../lib/decimals";
import { formatTokenAmount, formatUnixSeconds } from "../../lib/format";
import { httpsHref } from "../../lib/safeLink";
import {
  describeTxError,
  sendIncomeClaim,
  waitForTxReceipt,
} from "../../lib/wallet";

type Project = components["schemas"]["Project"];
type IncomeClaim = components["schemas"]["IncomeClaim"];

/** Lists wallet income entitlements and sends claim() with the server-supplied amount and proof */
export function IncomeSection({
  project,
  walletAddress,
  chainMismatch,
  onSubmitted,
}: {
  project: Project | undefined;
  walletAddress: string | null | undefined;
  chainMismatch: boolean;
  onSubmitted: (txHash: string, kind: string) => void;
}) {
  const distributor = project?.addresses?.incomeDistributor;
  const chainId = project?.chainId ?? 0;
  const claims = useAsync<IncomeClaim[]>(
    (signal) =>
      distributor && walletAddress
        ? api.listIncomeClaims(walletAddress, { signal })
        : Promise.resolve([]),
    [distributor, walletAddress],
  );
  const [selected, setSelected] = useState<IncomeClaim | null>(null);
  // Set on Claim when decimals had to be read on-chain; the loaded project value applies before that
  const [resolvedDecimals, setResolvedDecimals] = useState<number>();
  const quoteDecimals = resolvedDecimals ?? project?.quoteDecimals;
  const [txState, setTxState] = useState<ConfirmationState>("draft");
  const [claimTx, setClaimTx] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Suppress repeat claims until indexing catches up, avoiding AlreadyClaimed reverts
  const [claimedHere, setClaimedHere] = useState<ReadonlySet<string>>(
    () => new Set(),
  );
  const paused = Boolean(project?.paused);

  if (!distributor || !walletAddress) return null;

  async function handleSelect(claim: IncomeClaim) {
    setSelected(claim);
    setClaimTx(null);
    setError(null);
    setTxState("draft");
    // Missing display decimals falls back to grouped raw units
    try {
      setResolvedDecimals(await resolveQuoteDecimals(project, chainId));
    } catch {
      setResolvedDecimals(undefined);
    }
  }

  async function handleClaim() {
    if (!selected || !walletAddress || !distributor) return;
    setTxState("awaiting-signature");
    setError(null);
    try {
      const tx = await sendIncomeClaim(
        chainId,
        walletAddress as `0x${string}`,
        distributor as `0x${string}`,
        BigInt(selected.distributionId),
        BigInt(selected.amount),
        selected.proof as `0x${string}`[],
      );
      setClaimTx(tx);
      onSubmitted(tx, "income claim");
      setTxState("submitted");
      await waitForTxReceipt(tx as `0x${string}`);
      setClaimedHere((ids) => new Set(ids).add(selected.distributionId));
      setSelected(null);
      claims.reload();
    } catch (err) {
      setTxState("draft");
      setError(describeTxError(err, "Claim failed.", "income"));
    }
  }

  return (
    <section className="card" aria-label="Income">
      <h2>Income</h2>
      <AsyncSection
        state={claims}
        onRetry={claims.reload}
        empty={(items) => items.length === 0}
        emptyLabel="No income entitlements for this wallet."
      >
        {(items) => (
          <ul>
            {items.map((c) => {
              const href = httpsHref(c.snapshotURI);
              const status =
                c.status === "Claimable" && claimedHere.has(c.distributionId)
                  ? "Claimed"
                  : c.status;
              return (
                <li key={c.distributionId}>
                  Distribution {c.distributionId}:{" "}
                  {formatTokenAmount(c.amount, quoteDecimals)} quote tokens,{" "}
                  {status}, claim by {formatUnixSeconds(c.claimDeadline)}.
                  Snapshot:{" "}
                  {href ? (
                    <a href={href} rel="noopener noreferrer">
                      {c.snapshotURI}
                    </a>
                  ) : (
                    <span className="mono">{c.snapshotURI}</span>
                  )}
                  {c.status === "Claimed" && c.claimTxHash && (
                    <>
                      {" "}
                      Claim tx: <span className="mono">{c.claimTxHash}</span>
                    </>
                  )}
                  {status === "Claimable" && (
                    <button
                      type="button"
                      className="button button--secondary"
                      onClick={() => void handleSelect(c)}
                      disabled={chainMismatch || paused}
                    >
                      Claim
                    </button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </AsyncSection>
      {paused && (
        <p className="field__hint" role="status">
          The project is paused; claims resume when it is unpaused.
        </p>
      )}

      {selected && (
        <TransactionPreview
          title="Income claim preview"
          chainId={chainId}
          addresses={[
            {
              label: "Income distributor (transaction target)",
              address: distributor,
            },
          ]}
          amounts={[
            {
              label: `Claim amount (distribution ${selected.distributionId})`,
              raw: selected.amount,
              decimals: quoteDecimals,
              symbol: "quote token",
            },
          ]}
          confirmationState={txState}
        >
          <button
            type="button"
            className="button button--primary"
            onClick={() => void handleClaim()}
            disabled={
              chainMismatch || paused || txState === "awaiting-signature"
            }
          >
            Confirm claim
          </button>
        </TransactionPreview>
      )}
      {claimTx && (
        <p role="status">
          Claim submitted: <span className="mono">{claimTx}</span>
        </p>
      )}
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
    </section>
  );
}
