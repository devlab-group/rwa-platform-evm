import { useCallback, useState } from "react";
import { AsyncSection } from "../../components/AsyncSection";
import { PaginationFooter } from "../../components/PaginationFooter";
import {
  RedemptionStatusBadge,
  TransactionStatusBadge,
} from "../../components/StatusBadge";
import {
  TransactionPreview,
  type ConfirmationState,
} from "../../components/TransactionPreview";
import { useAsync } from "../../hooks/useAsync";
import { usePaginatedList } from "../../hooks/usePaginatedList";
import { useWallet } from "../../hooks/useWallet";
import { KycVerificationSection } from "./KycVerification";
import { api, ApiError } from "../../lib/client";
import type { components } from "../../lib/api-types";
import { resolveQuoteDecimals, resolveRwaDecimals } from "../../lib/decimals";
import {
  formatTokenAmount,
  formatUnixSeconds,
  shortenAddress,
  toMinimalUnits,
} from "../../lib/format";
import {
  applySlippageCeil,
  applySlippageFloor,
  deadlineInMinutes,
  MAX_SLIPPAGE_BPS,
} from "../../lib/slippage";
import {
  formatWithDecimals,
  readErc20Allowance,
  readErc20Balance,
  readErc20Decimals,
  readPreviewBuy,
  readPreviewRedeem,
  sendBuy,
  sendCancelRedemption,
  sendClaimRedemption,
  sendErc20Approve,
  sendErc20Transfer,
  sendRequestRedemption,
  signMessage,
  waitForTxReceipt,
} from "../../lib/wallet";
import {
  clearWalletSession,
  getWalletSession,
  setWalletSession,
} from "../../lib/walletSession";

type Project = components["schemas"]["Project"];
type WalletStatus = components["schemas"]["WalletStatus"];
type VerifyChallengeResult = components["schemas"]["VerifyChallengeResult"];
type Redemption = components["schemas"]["Redemption"];
type Transaction = components["schemas"]["Transaction"];
type Challenge = components["schemas"]["Challenge"];

/**
 * A price preview read from the chain — Vault.previewBuy or
 * RedemptionEscrow.previewRedeem — with the amount it was quoted for. Both
 * figures are minimal units: `tokenAmount` in RWA decimals, `quoteAmount` in
 * quote-token decimals. The slippage bound is derived from `quoteAmount`.
 */
interface Quote {
  tokenAmount: string;
  quoteAmount: string;
  side: "purchase" | "redemption";
}

/** A wallet-submitted transaction this browser session has seen (see HistorySection). */
interface SubmittedTx {
  txHash: string;
  kind: string;
  submittedAt: number;
}

/**
 * Investor: wallet connect + ownership challenge, KYC status, balance &
 * compliance, purchase, redemption (request/cancel/funded/claim), transfer,
 * and transaction history.
 *
 * Buy/request/claim/cancel are encoded here in the browser from the pinned
 * Vault/RedemptionEscrow ABI fragments (lib/abis.ts) and broadcast from the
 * connected wallet (lib/wallet.ts), exactly like the ERC-20 approve/transfer
 * steps, and the buy/redeem quotes come from the same contracts' preview views.
 * The server supplies indexed lists and compliance state only — it never hands
 * the browser calldata or a price, so the target of every transaction is the
 * project address this page already knows and there is nothing to check a
 * server-picked target or quote against.
 */
export function Investor() {
  const project = useAsync<Project>((signal) => api.getProject({ signal }), []);
  const chainId =
    project.status === "success" ? (project.data.chainId ?? 0) : 0;
  // undefined (not 0) while the project hasn't loaded yet, so useWallet
  // doesn't flag a spurious mismatch before the expected chain is known.
  const expectedChainId = chainId || undefined;
  const wallet = useWallet(expectedChainId);
  const finalityConfirmations =
    project.status === "success"
      ? project.data.finalityConfirmations
      : undefined;

  // The investor's own KYC status comes from a subject-scoped session minted
  // at challenge-verify (X-Wallet-Session), never from the operator-only global
  // wallet list.
  const ownWalletStatus = useOwnWalletStatus(wallet.address);
  const submitted = useSubmittedTransactions();

  return (
    <div>
      <header className="app-main__header">
        <h1>Investor</h1>
        <p>
          Connect a wallet to view balance, compliance status, and submit
          transactions.
        </p>
      </header>

      <WalletSection
        wallet={wallet}
        expectedChainId={expectedChainId}
        onSessionEstablished={ownWalletStatus.reload}
      />

      <KycSection
        ownWalletStatus={ownWalletStatus}
        connected={Boolean(wallet.address)}
      />

      <KycVerificationSection
        walletAddress={wallet.address}
        ownWalletStatus={ownWalletStatus}
      />

      <BalanceSection
        project={project.status === "success" ? project.data : undefined}
        address={wallet.address}
        chainMismatch={wallet.chainMismatch}
      />

      <BuySection
        chainId={chainId}
        project={project.status === "success" ? project.data : undefined}
        walletAddress={wallet.address}
        chainMismatch={wallet.chainMismatch}
        onSubmitted={submitted.record}
      />

      <RedemptionSection
        chainId={chainId}
        project={project.status === "success" ? project.data : undefined}
        walletAddress={wallet.address}
        finalityConfirmations={finalityConfirmations}
        chainMismatch={wallet.chainMismatch}
        onSubmitted={submitted.record}
      />

      <TransferSection
        project={project.status === "success" ? project.data : undefined}
        walletAddress={wallet.address}
        chainMismatch={wallet.chainMismatch}
        onSubmitted={submitted.record}
      />

      <HistorySection
        entries={submitted.entries}
        walletAddress={wallet.address}
      />
    </div>
  );
}

/**
 * The connected wallet's own compliance status, read via the subject-scoped
 * session minted at challenge-verify (X-Wallet-Session) — never the
 * operator-only global wallet list. `data` is `null` (a successful
 * "no status" result, not an error) whenever there is no
 * unexpired session for `address` yet, i.e. the investor hasn't completed
 * (or has outlived) an ownership challenge.
 */
function useOwnWalletStatus(address: string | null) {
  return useAsync<WalletStatus | null>(
    async (signal) => {
      if (!address) return null;
      const session = getWalletSession(address);
      if (!session) return null;
      try {
        return await api.getMyWalletStatus(session.token, { signal });
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) {
          // Expired/invalid on the server even though our local copy looked
          // live — drop it so the UI prompts a fresh challenge instead of
          // retrying the same dead token forever.
          clearWalletSession(address);
          return null;
        }
        throw err;
      }
    },
    [address],
  );
}

/**
 * Locally-tracked record of every transaction this browser session has
 * submitted from the connected wallet. The server's transaction index is
 * operator-only and doesn't contain investor wallet-submitted transactions
 * anyway — this is deliberately not a replacement for that index, just an
 * honest "what you just sent" list.
 */
function useSubmittedTransactions() {
  const [entries, setEntries] = useState<SubmittedTx[]>([]);
  const record = useCallback((txHash: string, kind: string) => {
    setEntries((prev) => [{ txHash, kind, submittedAt: Date.now() }, ...prev]);
  }, []);
  return { entries, record };
}

function WalletSection({
  wallet,
  expectedChainId,
  onSessionEstablished,
}: {
  wallet: ReturnType<typeof useWallet>;
  expectedChainId: number | undefined;
  onSessionEstablished: () => void;
}) {
  const [challenge, setChallenge] = useState<Challenge | null>(null);
  const [signature, setSignature] = useState<string | null>(null);
  const [verified, setVerified] = useState<VerifyChallengeResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function handleChallenge() {
    if (!wallet.address) return;
    setError(null);
    setSignature(null);
    setVerified(null);
    setBusy(true);
    try {
      setChallenge(await api.createChallenge({ address: wallet.address }));
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Failed to create challenge.",
      );
    } finally {
      setBusy(false);
    }
  }

  async function handleSign() {
    if (!wallet.address || !challenge?.message) return;
    setError(null);
    setBusy(true);
    try {
      setSignature(await signMessage(wallet.address, challenge.message));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Signing failed.");
    } finally {
      setBusy(false);
    }
  }

  async function handleVerify() {
    if (!wallet.address || !challenge?.nonce || !signature) return;
    setError(null);
    setBusy(true);
    try {
      const result = await api.verifyChallenge({
        address: wallet.address,
        nonce: challenge.nonce,
        signature,
      });
      setVerified(result);
      // Store the subject-scoped session and have the KYC section re-read its
      // own status through it — never an operator key.
      if (result.sessionToken && result.sessionExpiresAt) {
        setWalletSession(wallet.address, {
          token: result.sessionToken,
          expiresAt: result.sessionExpiresAt,
        });
        onSessionEstablished();
      }
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Verification failed.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card">
      <h2>Wallet</h2>
      {!wallet.address ? (
        <button
          type="button"
          className="button button--primary"
          onClick={wallet.connect}
          disabled={wallet.connecting}
        >
          {wallet.connecting ? "Connecting…" : "Connect wallet"}
        </button>
      ) : (
        <dl className="tx-preview__grid">
          <div className="tx-preview__row">
            <dt>Address</dt>
            <dd title={wallet.address}>{shortenAddress(wallet.address)}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Wallet chain ID</dt>
            <dd>{wallet.chainId}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Project chain ID</dt>
            <dd>{expectedChainId ?? "—"}</dd>
          </div>
        </dl>
      )}
      {wallet.error && (
        <p className="async-state--error" role="alert">
          {wallet.error}
        </p>
      )}

      {wallet.address && wallet.chainMismatch && (
        <div className="async-state async-state--error u-mt-4" role="alert">
          <p>
            Wallet is on chain {wallet.chainId}, but this project requires chain{" "}
            {expectedChainId}. All actions are disabled until you switch
            networks.
          </p>
          <button
            type="button"
            className="button button--secondary"
            onClick={wallet.switchChain}
            disabled={wallet.switching}
          >
            {wallet.switching
              ? "Switching…"
              : `Switch wallet to chain ${expectedChainId}`}
          </button>
        </div>
      )}

      {wallet.address && (
        <div className="u-mt-4">
          <button
            type="button"
            className="button button--secondary"
            onClick={handleChallenge}
            disabled={busy}
          >
            Generate ownership challenge
          </button>
          {challenge?.message && (
            <>
              <p className="mono">{challenge.message}</p>
              <button
                type="button"
                className="button button--secondary"
                onClick={handleSign}
                disabled={busy}
              >
                Sign with wallet
              </button>
            </>
          )}
          {signature && (
            <>
              <p className="field__hint" role="status">
                Signed: <span className="mono">{signature}</span>
              </p>
              <button
                type="button"
                className="button button--primary"
                onClick={handleVerify}
                disabled={busy}
              >
                Submit signature for verification
              </button>
            </>
          )}
          {verified && (
            <p className="field__hint" role="status">
              Ownership verified: {verified.ownershipVerified ? "Yes" : "No"}.
              Compliance status: {verified.status ?? "Unknown"}.
            </p>
          )}
          {error && (
            <p className="async-state--error" role="alert">
              {error}
            </p>
          )}
        </div>
      )}
    </section>
  );
}

function KycSection({
  ownWalletStatus,
  connected,
}: {
  ownWalletStatus: ReturnType<typeof useOwnWalletStatus>;
  connected: boolean;
}) {
  const status =
    ownWalletStatus.status === "success" ? ownWalletStatus.data : undefined;

  return (
    <section className="card">
      <h2>KYC status</h2>
      {!connected ? (
        <p className="field__hint">
          Connect a wallet to view your compliance status.
        </p>
      ) : ownWalletStatus.status === "loading" ? (
        <p className="field__hint">Loading…</p>
      ) : ownWalletStatus.status === "error" ? (
        <p className="async-state--error" role="alert">
          {ownWalletStatus.error}
        </p>
      ) : status ? (
        <dl className="tx-preview__grid">
          <div className="tx-preview__row">
            <dt>Status</dt>
            <dd>{status.status}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Valid until</dt>
            <dd>{formatUnixSeconds(status.validUntil)}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Ownership verified</dt>
            <dd>{status.ownershipVerified ? "Yes" : "No"}</dd>
          </div>
        </dl>
      ) : (
        <p className="field__hint">
          Generate and sign an ownership challenge below (Wallet section) to
          view your compliance status — it&apos;s read through your own verified
          session, not a shared list.
        </p>
      )}
    </section>
  );
}

function BalanceSection({
  project,
  address,
  chainMismatch,
}: {
  project: Project | undefined;
  address: string | null | undefined;
  chainMismatch: boolean;
}) {
  const [balance, setBalance] = useState<{
    raw: string;
    decimals: number;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const token = project?.addresses?.token;
  const chainId = project?.chainId;
  const unit = project?.tokenUnit ?? "RWA";

  async function handleLoad() {
    if (!token || !address || !chainId) return;
    setLoading(true);
    setError(null);
    try {
      const tokenAddress = token as `0x${string}`;
      // Project.decimals is now authoritative; only fall back to an on-chain
      // read if an older server build omits it.
      const decimals =
        project?.decimals ?? (await readErc20Decimals(chainId, tokenAddress));
      const raw = await readErc20Balance(
        chainId,
        tokenAddress,
        address as `0x${string}`,
      );
      setBalance({ raw, decimals });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to read balance.");
    } finally {
      setLoading(false);
    }
  }

  return (
    <section className="card">
      <h2>Balance</h2>
      {!address ? (
        <p className="field__hint">
          Connect a wallet to view your RWA token balance.
        </p>
      ) : !token ? (
        <p className="field__hint">
          Token address not yet available from the project.
        </p>
      ) : (
        <div>
          <button
            type="button"
            className="button button--secondary"
            onClick={handleLoad}
            disabled={loading || chainMismatch}
          >
            {loading ? "Reading…" : "Read balance"}
          </button>
          {balance !== null && (
            <p>
              {formatWithDecimals(balance.raw, balance.decimals)} {unit}
            </p>
          )}
          {error && (
            <p className="async-state--error" role="alert">
              {error}
            </p>
          )}
        </div>
      )}
    </section>
  );
}

function BuySection({
  chainId,
  project,
  walletAddress,
  chainMismatch,
  onSubmitted,
}: {
  chainId: number;
  project: Project | undefined;
  walletAddress: string | null | undefined;
  chainMismatch: boolean;
  onSubmitted: (txHash: string, kind: string) => void;
}) {
  const [tokenAmount, setTokenAmount] = useState("");
  const [slippageBps, setSlippageBps] = useState(50);
  const [quote, setQuote] = useState<Quote | null>(null);
  // Decimals captured at preview time so the preview renders the (already
  // minimal-unit) quote back as whole units: RWA decimals for the token
  // amount, quote-token decimals for the quote/payment amounts.
  const [decimals, setDecimals] = useState<number | null>(null);
  const [quoteDecimals, setQuoteDecimals] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [txState, setTxState] = useState<ConfirmationState>("draft");
  const [approveTx, setApproveTx] = useState<string | null>(null);
  const [allowance, setAllowance] = useState<bigint | null>(null);
  const [buyTx, setBuyTx] = useState<string | null>(null);

  const maxQuoteAmount = quote?.quoteAmount
    ? applySlippageCeil(quote.quoteAmount, slippageBps)
    : null;
  // The Vault pulls quoteToken (not the RWA token itself) on buy.
  const quoteToken = project?.addresses?.quoteToken;
  const vault = project?.addresses?.vault;
  const addressesReady = Boolean(chainId && quoteToken && vault);
  const allowanceSufficient =
    allowance !== null &&
    maxQuoteAmount !== null &&
    allowance >= BigInt(maxQuoteAmount);

  async function handlePreview() {
    setError(null);
    setBuyTx(null);
    try {
      if (!vault || !chainId) throw new Error("Project addresses not loaded.");
      // Human whole units -> the RWA token's minimal units before the amount
      // reaches the vault. toMinimalUnits throws a friendly AmountFormatError
      // on bad/over-precise input.
      const rwaDecimals = await resolveRwaDecimals(project, chainId);
      const minimalTokenAmount = toMinimalUnits(tokenAmount, rwaDecimals);
      // The price comes from the Vault itself, through the connected wallet —
      // the same contract the buy is sent to, on the same chain.
      const quoteAmount = await readPreviewBuy(
        chainId,
        vault as `0x${string}`,
        BigInt(minimalTokenAmount),
      );
      setDecimals(rwaDecimals);
      // Quote-token decimals are display-only and best-effort — a failed read
      // must not block the (already-correct) quote; fall back to the grouped
      // raw integer.
      try {
        setQuoteDecimals(await resolveQuoteDecimals(project, chainId));
      } catch {
        setQuoteDecimals(null);
      }
      setQuote({
        tokenAmount: minimalTokenAmount,
        quoteAmount: quoteAmount.toString(),
        side: "purchase",
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Quote request failed.");
    }
  }

  async function handleApprove() {
    if (!walletAddress || !addressesReady || !maxQuoteAmount) return;
    setTxState("awaiting-signature");
    setError(null);
    setAllowance(null);
    try {
      const tx = await sendErc20Approve(
        chainId,
        walletAddress as `0x${string}`,
        quoteToken as `0x${string}`,
        vault as `0x${string}`,
        BigInt(maxQuoteAmount),
      );
      setApproveTx(tx);
      onSubmitted(tx, "approve (quote token)");
      // Confirm the approval landed, then read the real on-chain allowance —
      // Buy stays disabled until it's actually sufficient.
      await waitForTxReceipt(tx as `0x${string}`);
      const current = await readErc20Allowance(
        chainId,
        quoteToken as `0x${string}`,
        walletAddress as `0x${string}`,
        vault as `0x${string}`,
      );
      setAllowance(current);
      setTxState("submitted");
    } catch (err) {
      setTxState("draft");
      setError(err instanceof Error ? err.message : "Approval failed.");
    }
  }

  async function handleBuy() {
    if (
      !walletAddress ||
      !quote?.tokenAmount ||
      !maxQuoteAmount ||
      !vault ||
      !chainId
    )
      return;
    setTxState("awaiting-signature");
    setError(null);
    try {
      // Encoded here and sent to this project's Vault — the buyer receives the
      // tokens themselves, bounded by the slippage ceiling they approved.
      const tx = await sendBuy(
        chainId,
        walletAddress as `0x${string}`,
        vault as `0x${string}`,
        {
          tokenAmount: BigInt(quote.tokenAmount),
          maxQuoteAmount: BigInt(maxQuoteAmount),
          recipient: walletAddress as `0x${string}`,
          deadline: BigInt(deadlineInMinutes(10)),
        },
      );
      setBuyTx(tx);
      onSubmitted(tx, "buy");
      setTxState("submitted");
    } catch (err) {
      setTxState("draft");
      setError(err instanceof Error ? err.message : "Buy failed.");
    }
  }

  return (
    <section className="card">
      <h2>Buy</h2>
      <div className="field">
        <label htmlFor="buy-token-amount">RWA token amount (whole units)</label>
        <input
          id="buy-token-amount"
          value={tokenAmount}
          onChange={(e) => setTokenAmount(e.target.value)}
        />
      </div>
      <div className="field field--slim">
        <label htmlFor="buy-slippage">Slippage (bps)</label>
        <input
          id="buy-slippage"
          type="number"
          min={0}
          max={MAX_SLIPPAGE_BPS}
          value={slippageBps}
          onChange={(e) => setSlippageBps(Number(e.target.value))}
          aria-describedby="buy-slippage-hint"
        />
        <span className="field__hint" id="buy-slippage-hint">
          Capped at {MAX_SLIPPAGE_BPS / 100}% — the approved spend amount never
          exceeds this.
        </span>
      </div>
      <button
        type="button"
        className="button button--secondary"
        onClick={handlePreview}
        disabled={!tokenAmount || chainMismatch}
      >
        Preview quote
      </button>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}

      {quote && project && maxQuoteAmount && (
        <TransactionPreview
          title="Purchase preview"
          chainId={chainId}
          addresses={[
            {
              label: "Vault (transaction target)",
              address: project.addresses?.vault ?? "",
            },
            { label: "RWA token", address: project.addresses?.token ?? "" },
          ]}
          amounts={[
            {
              label: "RWA amount",
              raw: quote.tokenAmount ?? "0",
              decimals: decimals ?? undefined,
              symbol: project.tokenUnit ?? "RWA",
            },
            {
              label: "Quote amount",
              raw: quote.quoteAmount ?? "0",
              decimals: quoteDecimals ?? undefined,
              symbol: "quote token",
            },
            {
              label: "Max quote amount (slippage bound)",
              raw: maxQuoteAmount,
              decimals: quoteDecimals ?? undefined,
              symbol: "quote token",
            },
          ]}
          priceSide="purchase"
          quoteTokenSymbol={
            project.addresses?.quoteToken
              ? shortenAddress(project.addresses.quoteToken)
              : undefined
          }
          slippageBps={slippageBps}
          confirmationState={txState}
        >
          <button
            type="button"
            className="button button--secondary"
            onClick={handleApprove}
            disabled={
              !walletAddress ||
              !addressesReady ||
              chainMismatch ||
              txState === "awaiting-signature"
            }
          >
            1. Approve quote token spend
          </button>
          <button
            type="button"
            className="button button--primary"
            onClick={handleBuy}
            disabled={
              !walletAddress ||
              !approveTx ||
              !allowanceSufficient ||
              chainMismatch ||
              txState === "awaiting-signature"
            }
          >
            2. Buy
          </button>
        </TransactionPreview>
      )}
      {approveTx && (
        <p role="status">
          Approval submitted: <span className="mono">{approveTx}</span>
        </p>
      )}
      {buyTx && (
        <p role="status">
          Buy submitted: <span className="mono">{buyTx}</span>
        </p>
      )}
    </section>
  );
}

function RedemptionSection({
  chainId,
  project,
  walletAddress,
  finalityConfirmations,
  chainMismatch,
  onSubmitted,
}: {
  chainId: number;
  project: Project | undefined;
  walletAddress: string | null | undefined;
  finalityConfirmations: number | undefined;
  chainMismatch: boolean;
  onSubmitted: (txHash: string, kind: string) => void;
}) {
  const [rwaAmount, setRwaAmount] = useState("");
  const [slippageBps, setSlippageBps] = useState(50);
  const [quote, setQuote] = useState<Quote | null>(null);
  // The entered RWA amount converted to minimal units at preview time — reused
  // by approve and request so all three see the same value the quote was built
  // from.
  const [minimalRwaAmount, setMinimalRwaAmount] = useState<string | null>(null);
  const [decimals, setDecimals] = useState<number | null>(null);
  const [quoteDecimals, setQuoteDecimals] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [txState, setTxState] = useState<ConfirmationState>("draft");
  const [approveTx, setApproveTx] = useState<string | null>(null);
  const [allowance, setAllowance] = useState<bigint | null>(null);
  const [requestTx, setRequestTx] = useState<string | null>(null);
  const [claimTx, setClaimTx] = useState<string | null>(null);
  const [cancelTx, setCancelTx] = useState<string | null>(null);
  // Request, claim and cancel are all RedemptionEscrow calls.
  const escrow = project?.addresses?.redemptionEscrow;

  // The connected wallet's own redemption requests, server-fetched and
  // paginated (the public listRedemptions filtered by beneficiary). Only
  // queried when a wallet is connected — disconnected shows a connect hint and
  // never hits the API.
  const myRedemptions = usePaginatedList<Redemption>(
    (cursor, signal) =>
      walletAddress
        ? api.listRedemptions({ address: walletAddress, cursor, signal })
        : Promise.resolve({ items: [] }),
    [walletAddress],
  );

  // Display decimals for the list rows: RWA from the project (authoritative);
  // the quote token has no HTTP-contract field, so it's read on-chain
  // best-effort and falls back to the grouped raw integer rather than
  // mis-scaling.
  const rwaDisplayDecimals = project?.decimals;
  const listQuoteDecimalsState = useAsync<number | null>(async () => {
    if (!project || !chainId) return null;
    try {
      return await resolveQuoteDecimals(project, chainId);
    } catch {
      return null;
    }
  }, [project?.addresses?.quoteToken, chainId]);
  const listQuoteDecimals =
    listQuoteDecimalsState.status === "success"
      ? (listQuoteDecimalsState.data ?? undefined)
      : undefined;

  const minQuoteOut = quote?.quoteAmount
    ? applySlippageFloor(quote.quoteAmount, slippageBps)
    : null;
  const allowanceSufficient =
    allowance !== null &&
    minimalRwaAmount !== null &&
    allowance >= BigInt(minimalRwaAmount);

  async function handlePreview() {
    setError(null);
    setRequestTx(null);
    // A re-quote invalidates the previous approval: its allowance covers the
    // old amount, not this one.
    setApproveTx(null);
    setAllowance(null);
    try {
      if (!escrow || !chainId) throw new Error("Project addresses not loaded.");
      // Human whole units -> the RWA token's minimal units before the amount
      // reaches the escrow or the approve/request call.
      const rwaDecimals = await resolveRwaDecimals(project, chainId);
      const minimal = toMinimalUnits(rwaAmount, rwaDecimals);
      // Priced by the escrow the request is sent to, read through the wallet.
      const quoteAmount = await readPreviewRedeem(
        chainId,
        escrow as `0x${string}`,
        BigInt(minimal),
      );
      setDecimals(rwaDecimals);
      setMinimalRwaAmount(minimal);
      try {
        setQuoteDecimals(await resolveQuoteDecimals(project, chainId));
      } catch {
        setQuoteDecimals(null);
      }
      setQuote({
        tokenAmount: minimal,
        quoteAmount: quoteAmount.toString(),
        side: "redemption",
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Quote request failed.");
    }
  }

  async function handleApprove() {
    if (
      !walletAddress ||
      !project?.addresses?.redemptionEscrow ||
      !minimalRwaAmount ||
      !chainId
    )
      return;
    setTxState("awaiting-signature");
    setError(null);
    setAllowance(null);
    try {
      const tx = await sendErc20Approve(
        chainId,
        walletAddress as `0x${string}`,
        project.addresses.token as `0x${string}`,
        project.addresses.redemptionEscrow as `0x${string}`,
        BigInt(minimalRwaAmount),
      );
      setApproveTx(tx);
      onSubmitted(tx, "approve (RWA token)");
      // Confirm the approval landed, then read the real on-chain allowance —
      // Request stays disabled until it's actually sufficient.
      await waitForTxReceipt(tx as `0x${string}`);
      const current = await readErc20Allowance(
        chainId,
        project.addresses.token as `0x${string}`,
        walletAddress as `0x${string}`,
        project.addresses.redemptionEscrow as `0x${string}`,
      );
      setAllowance(current);
      setTxState("submitted");
    } catch (err) {
      setTxState("draft");
      setError(err instanceof Error ? err.message : "Approval failed.");
    }
  }

  async function handleRequest() {
    if (
      !walletAddress ||
      !minimalRwaAmount ||
      !minQuoteOut ||
      !escrow ||
      !chainId
    )
      return;
    setTxState("awaiting-signature");
    setError(null);
    try {
      const tx = await sendRequestRedemption(
        chainId,
        walletAddress as `0x${string}`,
        escrow as `0x${string}`,
        {
          rwaAmount: BigInt(minimalRwaAmount),
          minQuoteOut: BigInt(minQuoteOut),
          deadline: BigInt(deadlineInMinutes(10)),
        },
      );
      setRequestTx(tx);
      onSubmitted(tx, "redemption request");
      setTxState("submitted");
    } catch (err) {
      setTxState("draft");
      setError(
        err instanceof Error ? err.message : "Redemption request failed.",
      );
    }
  }

  async function handleClaim(id: string) {
    if (!walletAddress || !escrow || !chainId) return;
    setError(null);
    try {
      const tx = await sendClaimRedemption(
        chainId,
        walletAddress as `0x${string}`,
        escrow as `0x${string}`,
        BigInt(id),
      );
      setClaimTx(tx);
      onSubmitted(tx, "redemption claim");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Claim failed.");
    }
  }

  async function handleCancel(id: string) {
    if (!walletAddress || !escrow || !chainId) return;
    setError(null);
    try {
      const tx = await sendCancelRedemption(
        chainId,
        walletAddress as `0x${string}`,
        escrow as `0x${string}`,
        BigInt(id),
      );
      setCancelTx(tx);
      onSubmitted(tx, "redemption cancel");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Cancel failed.");
    }
  }

  return (
    <section className="card">
      <h2>Redemption</h2>

      <div className="field">
        <label htmlFor="redeem-amount">
          RWA amount to redeem (whole units)
        </label>
        <input
          id="redeem-amount"
          value={rwaAmount}
          onChange={(e) => setRwaAmount(e.target.value)}
        />
      </div>
      <div className="field field--slim">
        <label htmlFor="redeem-slippage">Slippage (bps)</label>
        <input
          id="redeem-slippage"
          type="number"
          min={0}
          max={10_000}
          value={slippageBps}
          onChange={(e) => setSlippageBps(Number(e.target.value))}
        />
      </div>
      <button
        type="button"
        className="button button--secondary"
        onClick={handlePreview}
        disabled={!rwaAmount || chainMismatch}
      >
        Preview quote
      </button>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}

      {quote && project && minQuoteOut && (
        <TransactionPreview
          title="Redemption request preview"
          chainId={chainId}
          addresses={[
            {
              label: "Redemption escrow (transaction target)",
              address: project.addresses?.redemptionEscrow ?? "",
            },
            { label: "RWA token", address: project.addresses?.token ?? "" },
          ]}
          amounts={[
            {
              label: "RWA amount",
              raw: quote.tokenAmount ?? minimalRwaAmount ?? "0",
              decimals: decimals ?? undefined,
              symbol: project.tokenUnit ?? "RWA",
            },
            {
              label: "Snapshotted quote",
              raw: quote.quoteAmount ?? "0",
              decimals: quoteDecimals ?? undefined,
              symbol: "quote token",
            },
            {
              label: "Min quote out (slippage bound)",
              raw: minQuoteOut,
              decimals: quoteDecimals ?? undefined,
              symbol: "quote token",
            },
          ]}
          priceSide="redemption"
          quoteTokenSymbol={
            project.addresses?.quoteToken
              ? shortenAddress(project.addresses.quoteToken)
              : undefined
          }
          slippageBps={slippageBps}
          confirmationState={txState}
        >
          <button
            type="button"
            className="button button--secondary"
            onClick={handleApprove}
            disabled={
              !walletAddress ||
              chainMismatch ||
              txState === "awaiting-signature"
            }
          >
            1. Approve RWA spend
          </button>
          <button
            type="button"
            className="button button--primary"
            onClick={handleRequest}
            disabled={
              !walletAddress ||
              !approveTx ||
              !allowanceSufficient ||
              chainMismatch ||
              txState === "awaiting-signature"
            }
          >
            2. Request redemption
          </button>
        </TransactionPreview>
      )}
      {approveTx && (
        <p role="status">
          Approval submitted: <span className="mono">{approveTx}</span>
        </p>
      )}
      {requestTx && (
        <p role="status">
          Request submitted: <span className="mono">{requestTx}</span>
        </p>
      )}

      <h3 className="u-mt-5">Your redemption requests</h3>
      {!walletAddress ? (
        <p className="field__hint">
          Connect a wallet to view your redemption requests.
        </p>
      ) : (
        <AsyncSection
          state={myRedemptions}
          onRetry={myRedemptions.reload}
          empty={(d) => d.length === 0}
          emptyLabel="No redemption requests for this wallet yet."
        >
          {(data) => (
            <>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>ID</th>
                      <th>Status</th>
                      <th>RWA amount</th>
                      <th>Quote amount</th>
                      <th>Timeout</th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.map((r) => {
                      // Claim: server-derived `claimable` (Funded AND
                      // confirmations>=finality). Cancel: a Pending request
                      // whose timeout has elapsed (the beneficiary's escape
                      // hatch). Neither shows for Completed/Rejected/Cancelled
                      // or a Funded-but-not-yet-claimable request.
                      const cancellable =
                        r.status === "Pending" &&
                        !!r.timeoutAt &&
                        r.timeoutAt <= Math.floor(Date.now() / 1000);
                      return (
                        <tr key={r.id}>
                          <td className="mono">{r.id}</td>
                          <td>
                            <RedemptionStatusBadge
                              status={r.status}
                              claimable={r.claimable}
                              confirmations={r.confirmations}
                              finalityConfirmations={finalityConfirmations}
                            />
                          </td>
                          <td>
                            {formatTokenAmount(
                              r.rwaAmount ?? "",
                              rwaDisplayDecimals,
                            )}
                          </td>
                          <td>
                            {formatTokenAmount(
                              r.quoteAmount ?? "",
                              listQuoteDecimals,
                            )}
                          </td>
                          <td>{formatUnixSeconds(r.timeoutAt)}</td>
                          <td>
                            <span className="badge-row">
                              {r.claimable && r.id && (
                                <button
                                  type="button"
                                  className="button button--primary"
                                  onClick={() => handleClaim(r.id!)}
                                  disabled={!walletAddress || chainMismatch}
                                >
                                  Claim (permissionless)
                                </button>
                              )}
                              {cancellable && r.id && (
                                <button
                                  type="button"
                                  className="button button--danger"
                                  onClick={() => handleCancel(r.id!)}
                                  disabled={!walletAddress || chainMismatch}
                                >
                                  Cancel (timeout elapsed)
                                </button>
                              )}
                            </span>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
              <PaginationFooter
                loadedCount={data.length}
                totalCount={myRedemptions.totalCount}
                hasMore={myRedemptions.hasMore}
                loadingMore={myRedemptions.loadingMore}
                loadMoreError={myRedemptions.loadMoreError}
                onLoadMore={myRedemptions.loadMore}
              />
            </>
          )}
        </AsyncSection>
      )}
      {claimTx && (
        <p role="status">
          Claim submitted: <span className="mono">{claimTx}</span>
        </p>
      )}
      {cancelTx && (
        <p role="status">
          Cancel submitted: <span className="mono">{cancelTx}</span>
        </p>
      )}
    </section>
  );
}

function TransferSection({
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
  const [recipient, setRecipient] = useState("");
  const [amount, setAmount] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [tx, setTx] = useState<string | null>(null);
  const [sending, setSending] = useState(false);

  // Public, unauthenticated eligibility preflight — replaces the operator-only
  // global wallet list, which an investor session can never call. Deliberately
  // less detailed than before: AllowedResult is
  // just a boolean, not the recipient's full status/validUntil/ownership,
  // so an investor can't enumerate a third party's exact compliance record.
  const isWellFormedAddress = /^0x[0-9a-fA-F]{40}$/.test(recipient);
  const eligibility = useAsync<{ allowed?: boolean } | null>(
    (signal) =>
      isWellFormedAddress
        ? api.isAddressAllowed(recipient, { signal })
        : Promise.resolve(null),
    [recipient, isWellFormedAddress],
  );
  const recipientAllowed =
    eligibility.status === "success" && eligibility.data?.allowed === true;

  async function handleTransfer() {
    if (
      !walletAddress ||
      !project?.addresses?.token ||
      !project?.chainId ||
      !recipient ||
      !amount ||
      !recipientAllowed
    )
      return;
    setSending(true);
    setError(null);
    try {
      // Human whole units -> the RWA token's minimal units before building the
      // ERC-20 transfer calldata.
      const decimals = await resolveRwaDecimals(project, project.chainId);
      const minimal = toMinimalUnits(amount, decimals);
      const hash = await sendErc20Transfer(
        project.chainId,
        walletAddress as `0x${string}`,
        project.addresses.token as `0x${string}`,
        recipient as `0x${string}`,
        BigInt(minimal),
      );
      setTx(hash);
      onSubmitted(hash, "transfer");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Transfer failed.");
    } finally {
      setSending(false);
    }
  }

  return (
    <section className="card">
      <h2>Transfer</h2>
      <div className="field">
        <label htmlFor="transfer-recipient">Recipient address</label>
        <input
          id="transfer-recipient"
          className="mono"
          value={recipient}
          onChange={(e) => setRecipient(e.target.value)}
        />
      </div>
      {recipient && isWellFormedAddress && eligibility.status !== "loading" && (
        <p
          className={recipientAllowed ? "field__hint" : "async-state--error"}
          role={recipientAllowed ? undefined : "alert"}
        >
          {recipientAllowed
            ? "Recipient is eligible to receive tokens."
            : "Recipient is not eligible to receive tokens — transfer disabled."}
        </p>
      )}
      {recipient && !isWellFormedAddress && (
        <p className="field__hint">
          Enter a complete address to check eligibility.
        </p>
      )}
      <div className="field">
        <label htmlFor="transfer-amount">Amount (whole units)</label>
        <input
          id="transfer-amount"
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
        />
      </div>
      <button
        type="button"
        className="button button--primary"
        onClick={handleTransfer}
        disabled={
          sending ||
          !walletAddress ||
          chainMismatch ||
          !recipient ||
          !amount ||
          !recipientAllowed
        }
      >
        {sending ? "Submitting…" : "Transfer"}
      </button>
      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
      {tx && (
        <p role="status">
          Submitted: <span className="mono">{tx}</span>
        </p>
      )}
    </section>
  );
}

/**
 * Two complementary views of this wallet's activity:
 *
 * - "On-chain transactions": the server's now-public, address-filtered
 *   transaction index (`GET /api/v1/transactions?address=`), which carries the
 *   authoritative confirmation/reorg status the server observes. Only the
 *   connected wallet's own txs are queried.
 * - "Submitted this session": a local record of what this browser session sent
 *   from the wallet. The server index may not have indexed a just-submitted tx
 *   yet (or ever, for a pure wallet transfer), so this stays useful; it
 *   deliberately carries no confirmation status — this page can't observe it.
 */
function HistorySection({
  entries,
  walletAddress,
}: {
  entries: SubmittedTx[];
  walletAddress: string | null | undefined;
}) {
  // Server-indexed txs involving this wallet (public, paginated). Only queried
  // when connected — disconnected shows a connect hint and never hits the API.
  const onchain = usePaginatedList<Transaction>(
    (cursor, signal) =>
      walletAddress
        ? api.listTransactions({ address: walletAddress, cursor, signal })
        : Promise.resolve({ items: [] }),
    [walletAddress],
  );

  return (
    <section className="card">
      <h2>Transaction history</h2>

      <h3>On-chain transactions</h3>
      {!walletAddress ? (
        <p className="field__hint">
          Connect a wallet to view its transactions from the server&apos;s
          on-chain index.
        </p>
      ) : (
        <AsyncSection
          state={onchain}
          onRetry={onchain.reload}
          empty={(d) => d.length === 0}
          emptyLabel="No on-chain transactions indexed for this wallet yet."
        >
          {(data) => (
            <>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Tx hash</th>
                      <th>Kind</th>
                      <th>Status</th>
                      <th>Block</th>
                      <th>Explorer</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.map((tx) => (
                      <tr key={tx.txHash}>
                        <td className="mono">{tx.txHash}</td>
                        <td>{tx.kind ?? "—"}</td>
                        <td>
                          {tx.status && (
                            <TransactionStatusBadge status={tx.status} />
                          )}
                        </td>
                        <td>{tx.blockNumber ?? "—"}</td>
                        <td>
                          {tx.explorerUrl && (
                            <a
                              href={tx.explorerUrl}
                              target="_blank"
                              rel="noopener noreferrer"
                            >
                              View
                            </a>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <PaginationFooter
                loadedCount={data.length}
                totalCount={onchain.totalCount}
                hasMore={onchain.hasMore}
                loadingMore={onchain.loadingMore}
                loadMoreError={onchain.loadMoreError}
                onLoadMore={onchain.loadMore}
              />
            </>
          )}
        </AsyncSection>
      )}

      <h3 className="u-mt-5">Submitted this session</h3>
      {entries.length === 0 ? (
        <p className="field__hint">
          Nothing submitted from this wallet yet this session. Connect and
          submit a transaction above to see it listed here.
        </p>
      ) : (
        <>
          <p className="field__hint">
            Submitted directly from your wallet this session — not the
            server&apos;s transaction index above; a just-sent transaction may
            not be indexed yet.
          </p>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Tx hash</th>
                  <th>Kind</th>
                </tr>
              </thead>
              <tbody>
                {entries.map((entry) => (
                  <tr key={entry.txHash}>
                    <td className="mono">{entry.txHash}</td>
                    <td>{entry.kind}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </section>
  );
}
