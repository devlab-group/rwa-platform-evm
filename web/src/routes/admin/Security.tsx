import { useState } from "react";
// `isAddress` is called with strict:false throughout. Operators paste
// addresses from block explorers and spreadsheets, where the EIP-55 casing is
// often lost; the contract only cares that the address is well formed, so
// checking the shape and not the checksum is what these forms need.
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
import {
  describeWalletError,
  sendAcceptAdminTransfer,
  sendBeginAdminTransfer,
  sendForcedTransfer,
  sendRoleChange,
  sendSetFrozenTokens,
  sendSetPaused,
  sendSetStrategyPrice,
  waitForTxReceipt,
} from "../../lib/wallet";

type Addresses = components["schemas"]["Addresses"];

type Project = components["schemas"]["Project"];

type Enforcement = components["schemas"]["Enforcement"];

/**
 * `securityAsOfBlock` (number), `securityAsOfTime` (ISO string), and
 * `securityStale` (boolean) aren't in api/openapi.yaml yet, so we extend the
 * generated `Project` type locally rather than hand-editing api-types.ts.
 * Every read below is optional-chained, so an older server that hasn't shipped
 * these fields still renders — just without the staleness banner.
 */
type ProjectWithSecurityStaleness = Project & {
  securityAsOfBlock?: number;
  securityAsOfTime?: string;
  securityStale?: boolean;
};

/** "as of 3m ago" / "as of 2h ago" / "as of just now" — no external date lib needed for this granularity. */
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
  // Older server: none of these fields are present. Say nothing rather than
  // imply a guarantee we can't back up either way.
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
      {securityStale ? "Possibly stale — " : ""}
      as of
      {securityAsOfBlock !== undefined ? ` block ${securityAsOfBlock}` : ""}
      {age ? ` (${age})` : ""}
      {securityAsOfBlock === undefined && !age ? " an unknown point" : ""}. Not
      guaranteed current — reload to refresh.
    </p>
  );
}

/**
 * Security: pause status, role holders, auditor, treasury, redemption
 * manager, strategy, deployment bytecode/version.
 *
 * This data is a point-in-time snapshot from the last indexer read, not a
 * live chain read — it must never be presented as guaranteed current. See
 * SecurityStalenessNotice above.
 */
export function Security() {
  const project = useAsync<ProjectWithSecurityStaleness>(
    (signal) =>
      api.getProject({ signal }) as Promise<ProjectWithSecurityStaleness>,
    [],
  );

  // The live prices are quote-token minimal units, so they scale by the QUOTE
  // token's decimals (never the RWA token's). Prefer the server-provided
  // Project.quoteDecimals; fall back to an on-chain read only when it's absent.
  // A failed resolve leaves quoteDecimals undefined and formatTokenAmount shows
  // the grouped raw integer rather than mis-scaling.
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

  // Enforcement state is admin-gated and lives on its own endpoint, so it is a
  // second fetch rather than a field on the project. Reloaded together with the
  // project after any enforcement transaction confirms.
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
                  <dd>{data.version ?? "—"}</dd>
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
                  <dd>{data.decimals ?? "—"}</dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Token unit</dt>
                  <dd>{data.tokenUnit ?? "—"}</dd>
                </div>
                <div className="tx-preview__row">
                  <dt>Finality confirmations</dt>
                  <dd>{data.finalityConfirmations ?? "—"}</dd>
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
            <div className="table-wrap">
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
            </div>
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
              <div className="table-wrap">
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
              </div>
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

      {/*
        Not RoleGate-gated: the incoming admin is NOT DEFAULT_ADMIN yet (that's
        the whole point of accepting), so it never appears in proj.roles. Shown
        only while a transfer is pending (proj.pendingAdmin set); the control
        itself decides between the accept button (wallet matches pendingAdmin)
        and a read-only note (someone else is the incoming admin).
      */}
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

/** The connected wallet address, or null. All admin actions require it. */
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
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const token = project?.addresses?.token;
  const paused = Boolean(project?.paused);

  async function handle(pause: boolean) {
    setError(null);
    setDone(null);
    if (!from || !token) {
      setError("Wallet not connected or token address unavailable.");
      return;
    }
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
        . Pausing halts all token transfers (buys, redemptions, and wallet
        transfers) until unpaused.
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
  const [purchase, setPurchase] = useState("");
  const [redemption, setRedemption] = useState("");
  const [busy, setBusy] = useState<"purchase" | "redemption" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const strategy = project?.addresses?.strategy;

  async function handle(side: "purchase" | "redemption", value: string) {
    setError(null);
    setDone(null);
    if (!from || !strategy) {
      setError("Wallet not connected or strategy address unavailable.");
      return;
    }
    // Prices are entered in whole quote-token units and scaled to the quote
    // token's minimal units (per whole RWA token) before going on-chain. The
    // decimals come from the server (Project.quoteDecimals) or an on-chain
    // read; a failed resolve blocks the tx rather than mis-scaling.
    let decimals = quoteDecimals;
    if (decimals === undefined) {
      try {
        decimals = await resolveQuoteDecimals(project, chainId);
      } catch {
        setError(
          "Couldn't determine the quote token's decimals — connect your wallet to the project chain and try again.",
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

/**
 * Sends `send` to each target contract in turn (a role may live on more than
 * one contract), waiting for each receipt. Returns a per-contract error message
 * if any submission fails, naming how many succeeded so the operator knows the
 * partial state.
 */
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

/**
 * Parses a human-entered RWA amount into the token's minimal units, or returns
 * the reason it can't be. The token's decimals come from the project record; a
 * project that has not reported them blocks the transaction rather than
 * guessing a scale.
 */
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

/**
 * Whether `value` is the Vault or the redemption escrow. Early feedback only: the contract
 * refuses both a freeze and a seizure on either one, since they hold the unsold float and
 * already-funded redemptions on the whole project's behalf.
 */
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

/** Both enforcement calls sit in a public mempool long enough for the target to react. */
function FrontRunningWarning() {
  return (
    <p className="field__hint">
      This transaction is visible in the mempool before it lands, so a holder
      watching for it can move their balance first. Pause the project and
      confirm the pause landed before freezing or seizing, then unpause:
      ordinary transfers stop while these two calls keep working. On a chain
      where a pause is too disruptive, submit through a private relay instead.
    </p>
  );
}

/**
 * ERC-7943 setFrozenTokens: withholds part (or all) of a holder's balance
 * without moving it. The amount is ABSOLUTE, which the copy here has to make
 * unmissable - an operator who reads it as "freeze this much more" would
 * release tokens they meant to hold.
 */
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
    if (!from || !token) {
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

/**
 * ERC-7943 forcedTransfer: moves tokens out of a holder who cannot or will not
 * sign. Two-step on purpose - the confirmation spells out which protections
 * this bypasses, since nothing about the form itself signals that it moves
 * someone else's tokens.
 */
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
  const [holder, setHolder] = useState("");
  const [recipient, setRecipient] = useState("");
  const [amount, setAmount] = useState("");
  // The reviewed values, frozen at the moment the operator asked to confirm.
  // The confirmation has to be about a specific transfer, and it has to be the
  // one that gets sent: a mistyped recipient is the mistake this step exists
  // to catch, and re-reading the live inputs on confirm would let an edit slip
  // past the panel the operator just read.
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

  /** Validates the form and returns the parsed amount, or null after setting an error. */
  function validate(): bigint | null {
    if (!sender || !token) {
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
            <strong>This moves someone else&apos;s tokens.</strong> It goes
            through even if the holder is blocked or expired in the compliance
            registry, and even while trading is paused. It also reduces their
            frozen amount if it reaches into frozen tokens. The recipient must
            still be Allowed on-chain, and the transfer reverts otherwise.
          </p>
          <p>
            If the holder may be watching the mempool, pause the project first
            and confirm the pause landed: this call still works while paused,
            ordinary transfers do not.
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
    if (!from) {
      setError("Wallet not connected.");
      return;
    }
    const targets = roleTargets(role, addresses);
    if (targets.length === 0) {
      setError("No target contracts for this role — project not deployed?");
      return;
    }
    const hashRole = roleHash(role);
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
    if (!from) {
      setError("Wallet not connected.");
      return;
    }
    const targets = adminTransferTargets(addresses);
    if (targets.length === 0) {
      setError("No governance contracts found — project not deployed?");
      return;
    }
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
        Begins a two-step DEFAULT_ADMIN transfer on every governance contract.
        The new admin must then accept on each contract after the configured
        on-chain delay — until then, you remain admin.
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

/**
 * Step 2 of the two-step DEFAULT_ADMIN transfer, for the INCOMING admin. Renders
 * only when `project.pendingAdmin` is set (gated by the caller). If the connected
 * wallet is that pending admin, it offers an "Accept admin role" button that
 * calls acceptDefaultAdminTransfer() on every governance contract in turn
 * (DEFAULT_ADMIN lives on each). Otherwise it shows a read-only note naming who
 * must accept. Acceptance reverts on-chain until the configured delay elapses.
 */
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
      setError("No governance contracts found — project not deployed?");
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

  if (!isIncomingAdmin) {
    return (
      <p className="field__hint">
        An admin transfer is pending to{" "}
        <span className="mono" title={pendingAdmin}>
          {pendingAdmin ? shortenAddress(pendingAdmin) : "—"}
        </span>
        . The incoming admin must accept it.
      </p>
    );
  }

  return (
    <div>
      <p className="field__hint">
        A two-step admin transfer is pending to your wallet. Accepting completes
        it and makes you the DEFAULT_ADMIN — you must accept on every governance
        contract. This reverts if the configured on-chain delay has not elapsed
        yet; if so, wait and try again.
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
