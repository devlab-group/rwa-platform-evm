import { useState } from "react";
// Accept well-formed addresses without requiring preserved EIP-55 casing
import { isAddress, type Address } from "viem";
import { AsyncSection } from "../../components/AsyncSection";
import { RoleGate } from "../../components/RoleGate";
import { useAsync } from "../../hooks/useAsync";
import { useWalletContext } from "../../context/walletContextValue";
import { api } from "../../lib/client";
import type { components } from "../../lib/api-types";
import { resolveQuoteDecimals } from "../../lib/decimals";
import {
  formatTokenAmount,
  shortenAddress,
  toMinimalUnits,
} from "../../lib/format";
import {
  adminTransferTargets,
  GRANTABLE_ROLES,
  ROLES,
  roleHash,
  roleTargets,
} from "../../lib/roles";
import { exportSafeBatch } from "../../lib/safeBatch";
import { useTxRoute } from "../../lib/txRoute";
import {
  acceptAdminTransferCall,
  beginAdminTransferCall,
  describeWalletError,
  forcedTransferCall,
  pauseCall,
  roleChangeCall,
  setFrozenTokensCall,
  strategyPriceCall,
  sendAcceptAdminTransfer,
  sendBeginAdminTransfer,
  sendForcedTransfer,
  sendRoleChange,
  sendSetFrozenTokens,
  sendSetPaused,
  sendSetStrategyPrice,
  waitForTxReceipt,
} from "../../lib/wallet";
import { TableWrap } from "../../components/TableWrap";

type Addresses = components["schemas"]["Addresses"];

type Project = components["schemas"]["Project"];

type Enforcement = components["schemas"]["Enforcement"];

// Optional indexer snapshot metadata; servers without it remain compatible
type ProjectWithSecurityStaleness = Project & {
  securityAsOfBlock?: number;
  securityAsOfTime?: string;
  securityStale?: boolean;
};

// formatRelativeAge formats a valid timestamp as a short elapsed age
function formatRelativeAge(isoTime: string): string | undefined {
  const then = new Date(isoTime).getTime();
  if (Number.isNaN(then)) return undefined;
  const seconds = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (seconds < 60) return "just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return `${days}d ago`;
}

function SecurityStalenessNotice({
  data,
}: {
  data: ProjectWithSecurityStaleness;
}) {
  const { securityAsOfBlock, securityAsOfTime, securityStale } = data;
  // Do not imply freshness when the server supplies no snapshot metadata
  if (
    securityAsOfBlock === undefined &&
    securityAsOfTime === undefined &&
    securityStale === undefined
  ) {
    return null;
  }

  const age = securityAsOfTime
    ? formatRelativeAge(securityAsOfTime)
    : undefined;

  return (
    <p
      className={`security-staleness ${securityStale ? "security-staleness--stale" : ""}`}
      role={securityStale ? "alert" : "status"}
    >
      {securityStale ? "Possibly stale - " : ""}
      as of
      {securityAsOfBlock !== undefined ? ` block ${securityAsOfBlock}` : ""}
      {age ? ` (${age})` : ""}
      {securityAsOfBlock === undefined && !age ? " an unknown point" : ""}. Not
      guaranteed current - reload to refresh.
    </p>
  );
}

/**
 * Shows indexed governance and enforcement snapshots with freshness metadata.
 * Actions submit wallet transactions or export Safe batches according to their route.
 */
export function Security() {
  const project = useAsync<ProjectWithSecurityStaleness>(
    (signal) =>
      api.getProject({ signal }) as Promise<ProjectWithSecurityStaleness>,
    [],
  );

  // Resolve quote-token decimals from the project or chain; failed resolution
  // leaves display in raw units rather than using RWA decimals
  const proj = project.status === "success" ? project.data : undefined;
  const chainId = proj?.chainId ?? 0;
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

  // Fetch admin-only enforcement separately and reload it with project state after writes
  const enforcement = useAsync<Enforcement>(
    (signal) => api.getEnforcement({ signal }),
    [],
  );
  const reloadAll = () => {
    project.reload();
    enforcement.reload();
  };
  const lastForcedTransfer =
    enforcement.status === "success"
      ? enforcement.data.lastForcedTransfer
      : undefined;

  return (
    <div>
      <header className="app-main__header">
        <h1>Security</h1>
        <p>Pause status, roles, and deployment identity.</p>
      </header>

      <section className="card">
        <h2>Status</h2>
        <AsyncSection state={project} onRetry={project.reload}>
          {(data) => (
            <>
              <SecurityStalenessNotice data={data} />
              <dl className="tx-preview__grid">
                <div className="tx-preview__row">
                  <dt>Paused</dt>
                  <dd>
                    <span
                      className={`badge ${data.paused ? "badge--danger" : "badge--success"}`}
                    >
                      {data.paused ? "Paused" : "Active"}
                    </span>
                  </dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Version</dt>
                  <dd>{data.version ?? "-"}</dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Bytecode verified</dt>
                  <dd>
                    <span
                      className={`badge ${data.bytecodeVerified ? "badge--success" : "badge--warning"}`}
                    >
                      {data.bytecodeVerified ? "Verified" : "Unverified"}
                    </span>
                  </dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Decimals</dt>
                  <dd>{data.decimals ?? "-"}</dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Token unit</dt>
                  <dd>{data.tokenUnit ?? "-"}</dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Finality confirmations</dt>
                  <dd>{data.finalityConfirmations ?? "-"}</dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Auditor</dt>
                  <dd title={data.auditor}>{shortenAddress(data.auditor)}</dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Treasury</dt>
                  <dd title={data.treasury}>{shortenAddress(data.treasury)}</dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Redemption manager</dt>
                  <dd title={data.redemptionManager}>
                    {shortenAddress(data.redemptionManager)}
                  </dd>
                </div>
                {data.addresses &&
                  Object.entries(data.addresses).map(([key, value]) => (
                    <div className="tx-preview__row" key={key}>
                      <dt>{key}</dt>
                      <dd title={value}>{shortenAddress(value)}</dd>
                    </div>
                  ))}
              </dl>
            </>
          )}
        </AsyncSection>
      </section>

      <section className="card">
        <h2>Pause / unpause trading</h2>
        <RoleGate
          roles={proj?.roles}
          role={ROLES.pauser}
          action="pause or unpause trading"
        >
          <PauseControls
            project={proj}
            chainId={chainId}
            onChanged={project.reload}
          />
        </RoleGate>
      </section>

      <section className="card">
        <h2>Current prices</h2>
        <AsyncSection state={project} onRetry={project.reload}>
          {(data) =>
            data.purchasePricePerWholeToken === undefined &&
            data.redemptionPricePerWholeToken === undefined ? (
              <p className="field__hint">
                No on-chain prices reported yet (project not deployed or price
                not projected).
              </p>
            ) : (
              <dl className="tx-preview__grid">
                {data.purchasePricePerWholeToken !== undefined && (
                  <div className="tx-preview__row">
                    <dt>Current purchase price (per whole token)</dt>
                    <dd>
                      {formatTokenAmount(
                        data.purchasePricePerWholeToken,
                        quoteDecimals,
                      )}
                    </dd>
                  </div>
                )}
                {data.redemptionPricePerWholeToken !== undefined && (
                  <div className="tx-preview__row">
                    <dt>Current redemption price (per whole token)</dt>
                    <dd>
                      {formatTokenAmount(
                        data.redemptionPricePerWholeToken,
                        quoteDecimals,
                      )}
                    </dd>
                  </div>
                )}
              </dl>
            )
          }
        </AsyncSection>
      </section>

      <section className="card">
        <h2>Update prices</h2>
        <RoleGate roles={proj?.roles} role={ROLES.pricer} action="set prices">
          <PriceControls
            project={proj}
            chainId={chainId}
            quoteDecimals={quoteDecimals}
            onChanged={project.reload}
          />
        </RoleGate>
      </section>

      <section className="card">
        <h2>Role holders</h2>
        <AsyncSection
          state={project}
          onRetry={project.reload}
          empty={(d) => !d.roles || Object.keys(d.roles).length === 0}
          emptyLabel="No role holders reported."
        >
          {(data) => (
            <TableWrap label="Role holders">
              <table>
                <thead>
                  <tr>
                    <th>Role</th>
                    <th>Holders</th>
                  </tr>
                </thead>
                <tbody>
                  {data.roles &&
                    Object.entries(data.roles).map(([role, holders]) => (
                      <tr key={role}>
                        <td className="mono">{role}</td>
                        <td>
                          {holders.map((h) => (
                            <div key={h} className="mono" title={h}>
                              {shortenAddress(h)}
                            </div>
                          ))}
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </TableWrap>
          )}
        </AsyncSection>
      </section>

      <section className="card">
        <h2>Manage roles &amp; admin</h2>
        <RoleGate
          roles={proj?.roles}
          role={ROLES.admin}
          action="change role holders or transfer admin"
        >
          <RoleAdminControls
            project={proj}
            chainId={chainId}
            onChanged={project.reload}
          />
        </RoleGate>
      </section>

      <section className="card">
        <h2>Frozen balances</h2>
        <AsyncSection
          state={enforcement}
          onRetry={enforcement.reload}
          empty={(d) => Object.keys(d.frozenBalances ?? {}).length === 0}
          emptyLabel="No tokens are frozen."
        >
          {(data) => (
            <>
              <SecurityStalenessNotice data={data} />
              <TableWrap label="Frozen balances">
                <table>
                  <thead>
                    <tr>
                      <th>Holder</th>
                      <th>Frozen amount</th>
                    </tr>
                  </thead>
                  <tbody>
                    {Object.entries(data.frozenBalances ?? {}).map(
                      ([holder, amount]) => (
                        <tr key={holder}>
                          <td className="mono" title={holder}>
                            {shortenAddress(holder)}
                          </td>
                          <td className="mono">
                            {formatTokenAmount(amount, proj?.decimals)}
                          </td>
                        </tr>
                      ),
                    )}
                  </tbody>
                </table>
              </TableWrap>
              <p className="field__hint">
                A frozen amount can exceed the holder&apos;s balance, which
                withholds tokens they have not received yet.
              </p>
            </>
          )}
        </AsyncSection>
      </section>

      {lastForcedTransfer ? (
        <section className="card">
          <h2>Last forced transfer</h2>
          <dl className="tx-preview__grid">
            <div className="tx-preview__row">
              <dt>From</dt>
              <dd className="mono" title={lastForcedTransfer.from}>
                {shortenAddress(lastForcedTransfer.from ?? "")}
              </dd>
            </div>
            <div className="tx-preview__row">
              <dt>To</dt>
              <dd className="mono" title={lastForcedTransfer.to}>
                {shortenAddress(lastForcedTransfer.to ?? "")}
              </dd>
            </div>
            <div className="tx-preview__row">
              <dt>Amount</dt>
              <dd className="mono">
                {formatTokenAmount(
                  lastForcedTransfer.amount ?? "",
                  proj?.decimals,
                )}
              </dd>
            </div>
            <div className="tx-preview__row">
              <dt>Block</dt>
              <dd className="mono">
                {lastForcedTransfer.blockNumber ?? "\u2014"}
              </dd>
            </div>
            <div className="tx-preview__row">
              <dt>Transaction</dt>
              <dd className="mono" title={lastForcedTransfer.txHash}>
                {shortenAddress(lastForcedTransfer.txHash ?? "")}
              </dd>
            </div>
          </dl>
          <p className="field__hint">
            The most recent seizure only. Every forced transfer is in the
            transaction list and on-chain.
          </p>
        </section>
      ) : null}

      <section className="card">
        <h2>Freeze tokens</h2>
        <RoleGate
          roles={proj?.roles}
          role={ROLES.admin}
          action="freeze a holder's tokens"
        >
          <FreezeControls
            project={proj}
            chainId={chainId}
            onChanged={reloadAll}
          />
        </RoleGate>
      </section>

      <section className="card">
        <h2>Forced transfer</h2>
        <RoleGate
          roles={proj?.roles}
          role={ROLES.admin}
          action="seize tokens from a holder"
        >
          <ForcedTransferControls
            project={proj}
            chainId={chainId}
            onChanged={reloadAll}
          />
        </RoleGate>
      </section>

      {/* Pending admins accept before acquiring the role, so this control sits outside RoleGate */}
      {proj?.pendingAdmin ? (
        <section className="card">
          <h2>Accept admin role</h2>
          <AcceptAdminControls
            project={proj}
            chainId={chainId}
            onChanged={project.reload}
          />
        </section>
      ) : null}
    </div>
  );
}

// useSender returns the connected wallet, which direct submissions require
function useSender(): Address | null {
  return useWalletContext().address;
}

function PauseControls({
  project,
  chainId,
  onChanged,
}: {
  project: Project | undefined;
  chainId: number;
  onChanged: () => void;
}) {
  const from = useSender();
  const route = useTxRoute();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const token = project?.addresses?.token;
  const paused = Boolean(project?.paused);

  async function handle(pause: boolean) {
    setError(null);
    setDone(null);
    if (!token || (route.kind === "wallet" && !from)) {
      setError("Wallet not connected or token address unavailable.");
      return;
    }
    if (route.kind === "safe") {
      setDone(
        exportSafeBatch(
          chainId,
          route.safe,
          pause ? "Pause trading" : "Unpause trading",
          [pauseCall(token as Address, pause)],
        ),
      );
      return;
    }
    if (!from) return;
    setBusy(true);
    try {
      const hash = await sendSetPaused(chainId, from, token as Address, pause);
      await waitForTxReceipt(hash);
      setDone(pause ? "Trading paused." : "Trading unpaused.");
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Transaction failed.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div>
      <p className="field__hint">
        Current status:{" "}
        <span
          className={`badge ${paused ? "badge--danger" : "badge--success"}`}
        >
          {paused ? "Paused" : "Active"}
        </span>
        . Pausing stops purchases, ordinary transfers and redemption requests,
        funding, claims and rejection. Timed-out cancellation and admin
        enforcement remain available.
      </p>
      <div className="tx-preview__actions">
        <button
          type="button"
          className="button button--danger"
          onClick={() => handle(true)}
          disabled={busy || paused}
        >
          {busy ? "Submitting…" : "Pause trading"}
        </button>
        <button
          type="button"
          className="button button--primary"
          onClick={() => handle(false)}
          disabled={busy || !paused}
        >
          {busy ? "Submitting…" : "Unpause trading"}
        </button>
      </div>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {done && <p role="status">{done}</p>}
    </div>
  );
}

function PriceControls({
  project,
  chainId,
  quoteDecimals,
  onChanged,
}: {
  project: Project | undefined;
  chainId: number;
  quoteDecimals: number | undefined;
  onChanged: () => void;
}) {
  const from = useSender();
  const route = useTxRoute();
  const [purchase, setPurchase] = useState("");
  const [redemption, setRedemption] = useState("");
  const [busy, setBusy] = useState<"purchase" | "redemption" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const strategy = project?.addresses?.strategy;

  async function handle(side: "purchase" | "redemption", value: string) {
    setError(null);
    setDone(null);
    if (!strategy || (route.kind === "wallet" && !from)) {
      setError("Wallet not connected or strategy address unavailable.");
      return;
    }
    // Require known quote decimals before converting the entered price to minimal units
    let decimals = quoteDecimals;
    if (decimals === undefined) {
      try {
        decimals = await resolveQuoteDecimals(project, chainId);
      } catch {
        setError(
          "Couldn't determine the quote token's decimals - connect your wallet to the project chain and try again.",
        );
        return;
      }
    }
    let minimal: string;
    try {
      minimal = toMinimalUnits(value, decimals);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Invalid price.");
      return;
    }
    if (route.kind === "safe") {
      setDone(
        exportSafeBatch(
          chainId,
          route.safe,
          side === "purchase" ? "Set purchase price" : "Set redemption price",
          [strategyPriceCall(strategy as Address, side, BigInt(minimal))],
        ),
      );
      return;
    }
    if (!from) return;
    setBusy(side);
    try {
      const hash = await sendSetStrategyPrice(
        chainId,
        from,
        strategy as Address,
        side,
        BigInt(minimal),
      );
      await waitForTxReceipt(hash);
      setDone(
        side === "purchase"
          ? "Purchase price updated."
          : "Redemption price updated.",
      );
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Transaction failed.");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div>
      <p className="field__hint">
        Prices are per whole token, in whole quote-token units. Each is set on
        the fixed-price strategy independently.
      </p>
      <div className="field">
        <label htmlFor="new-purchase-price">
          Purchase price (whole quote-token units)
        </label>
        <input
          id="new-purchase-price"
          value={purchase}
          onChange={(e) => setPurchase(e.target.value)}
        />
      </div>
      <button
        type="button"
        className="button button--secondary"
        onClick={() => handle("purchase", purchase)}
        disabled={busy !== null || !purchase}
      >
        {busy === "purchase" ? "Submitting…" : "Set purchase price"}
      </button>
      <div className="field u-mt-4">
        <label htmlFor="new-redemption-price">
          Redemption price (whole quote-token units)
        </label>
        <input
          id="new-redemption-price"
          value={redemption}
          onChange={(e) => setRedemption(e.target.value)}
        />
      </div>
      <button
        type="button"
        className="button button--secondary"
        onClick={() => handle("redemption", redemption)}
        disabled={busy !== null || !redemption}
      >
        {busy === "redemption" ? "Submitting…" : "Set redemption price"}
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

// runSequential submits and confirms each target, returning a failure with the completed count
async function runSequential(
  targets: Address[],
  send: (target: Address) => Promise<`0x${string}`>,
  onProgress: (done: number, total: number) => void,
): Promise<string | null> {
  for (let i = 0; i < targets.length; i++) {
    onProgress(i, targets.length);
    try {
      const hash = await send(targets[i]);
      await waitForTxReceipt(hash);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Transaction failed.";
      return `Stopped after ${i} of ${targets.length} transaction(s): ${msg}`;
    }
  }
  onProgress(targets.length, targets.length);
  return null;
}

// parseTokenAmount converts using known project decimals or returns a validation error
function parseTokenAmount(
  value: string,
  decimals: number | undefined,
): { units: bigint } | { error: string } {
  if (decimals === undefined) {
    return {
      error:
        "Token decimals unavailable - reload once the project is deployed.",
    };
  }
  try {
    return { units: BigInt(toMinimalUnits(value, decimals)) };
  } catch (err) {
    return {
      error: err instanceof Error ? err.message : "Invalid amount.",
    };
  }
}

// isSystemAddress identifies the Vault or escrow for early enforcement feedback;
// contracts reject positive freezes and seizure sources for these addresses
function isSystemAddress(
  value: string,
  addresses: Addresses | undefined,
): boolean {
  const target = value.toLowerCase();
  return (
    target === addresses?.vault?.toLowerCase() ||
    target === addresses?.redemptionEscrow?.toLowerCase()
  );
}

// Balances and eligibility may change before an enforcement call executes
function FrontRunningWarning() {
  return (
    <p className="field__hint">
      A holder may see this transaction in the mempool and move tokens first.
      Pause the project and confirm the pause before freezing or seizing, then
      unpause. These enforcement calls still work while ordinary transfers are
      paused. If pausing is too disruptive, use a private relay.
    </p>
  );
}

// FreezeControls replaces an absolute frozen amount; it does not add to the prior amount
function FreezeControls({
  project,
  chainId,
  onChanged,
}: {
  project: Project | undefined;
  chainId: number;
  onChanged: () => void;
}) {
  const from = useSender();
  const route = useTxRoute();
  const [account, setAccount] = useState("");
  const [amount, setAmount] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const token = project?.addresses?.token;
  const addresses = project?.addresses as Addresses | undefined;

  async function handle() {
    setError(null);
    setDone(null);
    if (!token || (route.kind === "wallet" && !from)) {
      setError("Wallet not connected or token address unavailable.");
      return;
    }
    if (!isAddress(account, { strict: false })) {
      setError(`"${account}" is not a valid address.`);
      return;
    }
    if (isSystemAddress(account, addresses)) {
      setError(
        "The Vault and the redemption escrow can never be frozen - freezing one would stop every buy, claim, and cancel.",
      );
      return;
    }
    const parsed = parseTokenAmount(amount, project?.decimals);
    if ("error" in parsed) {
      setError(parsed.error);
      return;
    }
    if (route.kind === "safe") {
      setDone(
        exportSafeBatch(chainId, route.safe, "Set frozen tokens", [
          setFrozenTokensCall(
            token as Address,
            account as Address,
            parsed.units,
          ),
        ]),
      );
      return;
    }
    if (!from) return;
    setBusy(true);
    try {
      const hash = await sendSetFrozenTokens(
        chainId,
        from,
        token as Address,
        account as Address,
        parsed.units,
      );
      await waitForTxReceipt(hash);
      setDone(
        parsed.units === 0n
          ? `Released every frozen token held against ${account}.`
          : `Frozen amount for ${account} set to ${amount}.`,
      );
      onChanged();
    } catch (err) {
      setError(describeWalletError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div>
      <p className="field__hint">
        Sets the holder&apos;s <strong>absolute</strong> frozen amount,
        replacing whatever was frozen before. It does not add to an existing
        hold. Enter 0 to release everything. Frozen tokens stay in the
        holder&apos;s wallet; they just cannot be sent.
      </p>
      <FrontRunningWarning />
      <div className="field">
        <label htmlFor="freeze-account">Holder address</label>
        <input
          id="freeze-account"
          value={account}
          onChange={(e) => setAccount(e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="freeze-amount">
          Absolute frozen amount (whole tokens)
        </label>
        <input
          id="freeze-amount"
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
        />
      </div>
      <button
        type="button"
        className="button button--danger"
        onClick={handle}
        disabled={busy || !account || amount === ""}
      >
        {busy ? "Submitting…" : "Set frozen amount"}
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

// ForcedTransferControls requires explicit review before moving another holder's tokens
// and displays the eligibility, freeze and pause protections the call bypasses
function ForcedTransferControls({
  project,
  chainId,
  onChanged,
}: {
  project: Project | undefined;
  chainId: number;
  onChanged: () => void;
}) {
  const sender = useSender();
  const route = useTxRoute();
  const [holder, setHolder] = useState("");
  const [recipient, setRecipient] = useState("");
  const [amount, setAmount] = useState("");
  // Keep the confirmed transfer bound to the reviewed values, even if inputs change
  const [pending, setPending] = useState<{
    holder: Address;
    recipient: Address;
    units: bigint;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const token = project?.addresses?.token;
  const addresses = project?.addresses as Addresses | undefined;

  // validate returns the parsed amount or sets an error and returns null
  function validate(): bigint | null {
    if (!token || (route.kind === "wallet" && !sender)) {
      setError("Wallet not connected or token address unavailable.");
      return null;
    }
    if (!isAddress(holder, { strict: false })) {
      setError(`"${holder}" is not a valid address.`);
      return null;
    }
    if (!isAddress(recipient, { strict: false })) {
      setError(`"${recipient}" is not a valid address.`);
      return null;
    }
    if (isSystemAddress(holder, addresses)) {
      setError(
        "The Vault and the redemption escrow cannot be seized from - their balances are the unsold float and redemptions that are already funded.",
      );
      return null;
    }
    if (holder.toLowerCase() === recipient.toLowerCase()) {
      setError(
        "Sender and recipient must differ - the contract rejects a forced transfer to self.",
      );
      return null;
    }
    const parsed = parseTokenAmount(amount, project?.decimals);
    if ("error" in parsed) {
      setError(parsed.error);
      return null;
    }
    return parsed.units;
  }

  function review() {
    setError(null);
    setDone(null);
    const units = validate();
    if (units === null) return;
    setPending({
      holder: holder as Address,
      recipient: recipient as Address,
      units,
    });
  }

  async function handle() {
    if (!pending) return;
    setError(null);
    if (route.kind === "safe") {
      setDone(
        exportSafeBatch(chainId, route.safe, "Forced transfer", [
          forcedTransferCall(
            token as Address,
            pending.holder,
            pending.recipient,
            pending.units,
          ),
        ]),
      );
      setPending(null);
      return;
    }
    setBusy(true);
    try {
      const hash = await sendForcedTransfer(
        chainId,
        sender as Address,
        token as Address,
        pending.holder,
        pending.recipient,
        pending.units,
      );
      await waitForTxReceipt(hash);
      setDone(
        `Moved ${amount} from ${pending.holder} to ${pending.recipient}.`,
      );
      setPending(null);
      onChanged();
    } catch (err) {
      setError(describeWalletError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div>
      <p className="field__hint">
        Moves tokens out of a holder&apos;s wallet without their signature, for
        a seizure or a court-ordered recovery.
      </p>
      <FrontRunningWarning />
      <div className="field">
        <label htmlFor="forced-from">From (holder)</label>
        <input
          id="forced-from"
          value={holder}
          disabled={pending !== null}
          onChange={(e) => setHolder(e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="forced-to">To (recipient)</label>
        <input
          id="forced-to"
          value={recipient}
          disabled={pending !== null}
          onChange={(e) => setRecipient(e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="forced-amount">Amount (whole tokens)</label>
        <input
          id="forced-amount"
          value={amount}
          disabled={pending !== null}
          onChange={(e) => setAmount(e.target.value)}
        />
      </div>
      {pending ? (
        <div className="tx-preview" role="alert">
          <dl className="tx-preview__grid">
            <div className="tx-preview__row">
              <dt>Seize from</dt>
              <dd className="mono">{pending.holder}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Send to</dt>
              <dd className="mono">{pending.recipient}</dd>
            </div>
            <div className="tx-preview__row">
              <dt>Amount</dt>
              <dd className="mono">
                {amount} ({pending.units.toString()} minimal units)
              </dd>
            </div>
          </dl>
          <p>
            <strong>This moves someone else&apos;s tokens.</strong> This works
            while paused and when the holder is blocked or expired. It reduces
            their frozen amount if it spends frozen tokens. The recipient must
            be Allowed or the call reverts.
          </p>
          <p>
            If the holder may watch the mempool, pause and confirm first. This
            call works while paused; ordinary transfers stop.
          </p>
          <div className="tx-preview__actions">
            <button
              type="button"
              className="button button--danger"
              onClick={handle}
              disabled={busy}
            >
              {busy ? "Submitting…" : "Confirm forced transfer"}
            </button>
            <button
              type="button"
              className="button button--secondary"
              onClick={() => setPending(null)}
              disabled={busy}
            >
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <button
          type="button"
          className="button button--danger"
          onClick={review}
          disabled={busy || !holder || !recipient || amount === ""}
        >
          Force transfer
        </button>
      )}
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {done && <p role="status">{done}</p>}
    </div>
  );
}

function RoleAdminControls({
  project,
  chainId,
  onChanged,
}: {
  project: Project | undefined;
  chainId: number;
  onChanged: () => void;
}) {
  const from = useSender();
  const route = useTxRoute();
  const addresses = project?.addresses as Addresses | undefined;
  const [role, setRole] = useState<string>(GRANTABLE_ROLES[0]);
  const [account, setAccount] = useState("");
  const [newAdmin, setNewAdmin] = useState("");
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  function begin() {
    setError(null);
    setDone(null);
    setProgress(null);
  }

  async function handleRoleChange(action: "grant" | "revoke") {
    begin();
    if (route.kind === "wallet" && !from) {
      setError("Wallet not connected.");
      return;
    }
    const targets = roleTargets(role, addresses);
    if (targets.length === 0) {
      setError("No target contracts for this role - project not deployed?");
      return;
    }
    const hashRole = roleHash(role);
    if (route.kind === "safe") {
      // Export all target calls for atomic execution
      setDone(
        exportSafeBatch(
          chainId,
          route.safe,
          `${action === "grant" ? "Grant" : "Revoke"} ${role}`,
          targets.map((target) =>
            roleChangeCall(target, action, hashRole, account as Address),
          ),
        ),
      );
      return;
    }
    if (!from) return;
    setBusy(true);
    const failure = await runSequential(
      targets,
      (target) =>
        sendRoleChange(
          chainId,
          from,
          target,
          action,
          hashRole,
          account as Address,
        ),
      (d, t) => setProgress(`Submitting ${Math.min(d + 1, t)} of ${t}…`),
    );
    setBusy(false);
    setProgress(null);
    if (failure) setError(failure);
    else {
      setDone(
        `${action === "grant" ? "Granted" : "Revoked"} ${role} ${
          action === "grant" ? "to" : "from"
        } ${account} on ${targets.length} contract(s).`,
      );
      onChanged();
    }
  }

  async function handleTransferAdmin() {
    begin();
    if (route.kind === "wallet" && !from) {
      setError("Wallet not connected.");
      return;
    }
    const targets = adminTransferTargets(addresses);
    if (targets.length === 0) {
      setError("No governance contracts found - project not deployed?");
      return;
    }
    if (route.kind === "safe") {
      setDone(
        exportSafeBatch(
          chainId,
          route.safe,
          "Begin admin transfer",
          targets.map((target) =>
            beginAdminTransferCall(target, newAdmin as Address),
          ),
        ),
      );
      return;
    }
    if (!from) return;
    setBusy(true);
    const failure = await runSequential(
      targets,
      (target) =>
        sendBeginAdminTransfer(chainId, from, target, newAdmin as Address),
      (d, t) => setProgress(`Submitting ${Math.min(d + 1, t)} of ${t}…`),
    );
    setBusy(false);
    setProgress(null);
    if (failure) setError(failure);
    else {
      setDone(
        `Admin transfer to ${newAdmin} begun on ${targets.length} contract(s). The new admin must accept it on each contract after the on-chain delay to complete the transfer.`,
      );
      onChanged();
    }
  }

  return (
    <div>
      <h3>Grant or revoke a role</h3>
      <div className="field">
        <label htmlFor="role-select">Role</label>
        <select
          id="role-select"
          value={role}
          onChange={(e) => setRole(e.target.value)}
        >
          {GRANTABLE_ROLES.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </select>
        <span className="field__hint">
          Applied to every contract the role is held on (
          {roleTargets(role, addresses).length} for this role).
        </span>
      </div>
      <div className="field">
        <label htmlFor="role-account">Account address</label>
        <input
          id="role-account"
          className="mono"
          value={account}
          onChange={(e) => setAccount(e.target.value)}
        />
      </div>
      <div className="tx-preview__actions">
        <button
          type="button"
          className="button button--primary"
          onClick={() => handleRoleChange("grant")}
          disabled={busy || !account}
        >
          Grant role
        </button>
        <button
          type="button"
          className="button button--danger"
          onClick={() => handleRoleChange("revoke")}
          disabled={busy || !account}
        >
          Revoke role
        </button>
      </div>

      <h3 className="u-mt-5">Transfer admin</h3>
      <p className="field__hint">
        Begins a two-step DEFAULT_ADMIN transfer on each governance contract.
        You remain admin until the new admin accepts on that contract after its
        configured delay.
      </p>
      <div className="field">
        <label htmlFor="new-admin">New admin address</label>
        <input
          id="new-admin"
          className="mono"
          value={newAdmin}
          onChange={(e) => setNewAdmin(e.target.value)}
        />
      </div>
      <button
        type="button"
        className="button button--danger"
        onClick={handleTransferAdmin}
        disabled={busy || !newAdmin}
      >
        Begin admin transfer
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

// AcceptAdminControls completes a pending transfer after its on-chain delay.
// The matching wallet submits sequentially; a Safe acceptance batch can be exported
// for its owners when another wallet is connected.
function AcceptAdminControls({
  project,
  chainId,
  onChanged,
}: {
  project: Project | undefined;
  chainId: number;
  onChanged: () => void;
}) {
  const from = useSender();
  const addresses = project?.addresses as Addresses | undefined;
  const pendingAdmin = project?.pendingAdmin;
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  const isIncomingAdmin =
    !!from &&
    !!pendingAdmin &&
    from.toLowerCase() === pendingAdmin.toLowerCase();

  async function handleAccept() {
    setError(null);
    setDone(null);
    setProgress(null);
    if (!from) {
      setError("Wallet not connected.");
      return;
    }
    const targets = adminTransferTargets(addresses);
    if (targets.length === 0) {
      setError("No governance contracts found - project not deployed?");
      return;
    }
    setBusy(true);
    const failure = await runSequential(
      targets,
      (target) => sendAcceptAdminTransfer(chainId, from, target),
      (d, t) => setProgress(`Submitting ${Math.min(d + 1, t)} of ${t}…`),
    );
    setBusy(false);
    setProgress(null);
    if (failure) setError(failure);
    else {
      setDone(
        `Accepted the admin role on ${targets.length} contract(s). You are now the DEFAULT_ADMIN.`,
      );
      onChanged();
    }
  }

  // Export acceptance across configured governance contracts for Safe owner approval
  function handleExportAccept() {
    setError(null);
    setDone(null);
    const targets = adminTransferTargets(addresses);
    if (!pendingAdmin || targets.length === 0) {
      setError("No governance contracts found - project not deployed?");
      return;
    }
    setDone(
      exportSafeBatch(
        chainId,
        pendingAdmin as Address,
        "Accept admin role",
        targets.map(acceptAdminTransferCall),
      ),
    );
  }

  if (!isIncomingAdmin) {
    return (
      <div>
        <p className="field__hint">
          An admin transfer is pending to{" "}
          <span className="mono" title={pendingAdmin}>
            {pendingAdmin ? shortenAddress(pendingAdmin) : "-"}
          </span>
          . The incoming admin must accept after the on-chain delay. For a Safe,
          save a Safe{"{Wallet}"} acceptance batch for its owners to approve and
          execute.
        </p>
        <button
          type="button"
          className="button button--secondary"
          onClick={handleExportAccept}
        >
          Save acceptance batch for the incoming admin
        </button>
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

  return (
    <div>
      <p className="field__hint">
        Accept the pending transfer to become DEFAULT_ADMIN on each governance
        contract. If its configured delay has not elapsed, wait and try again.
      </p>
      <button
        type="button"
        className="button button--primary"
        onClick={handleAccept}
        disabled={busy}
      >
        {busy ? "Submitting…" : "Accept admin role"}
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
