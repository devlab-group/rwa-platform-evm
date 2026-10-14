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
import { LegalDisclosure } from "./LegalDisclosure";
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
  COMPLIANCE_STATUS_LABEL,
  complianceDisplayStatus,
} from "../../lib/status";
import {
  formatWithDecimals,
  readErc20Allowance,
  describeTxError,
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
 * A price preview read from the chain (Vault.previewBuy or
 * RedemptionEscrow.previewRedeem) with the amount it was quoted for. Both
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
 * Investor wallet ownership, verification, balances, purchase, redemption,
 * transfer and transaction history. lib/wallet.ts encodes calls from pinned
 * ABIs and broadcasts through the connected wallet; quotes come from Vault
 * and escrow preview views. The server supplies configuration, indexed lists
 * and compliance state, rather than wallet calldata or quotes.
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

  // The investor's own KYC status comes from the X-Wallet-Session minted at
  // challenge-verify, not from the admin-only wallet list
  const ownWalletStatus = useOwnWalletStatus(wallet.address);
  const submitted = useSubmittedTransactions();

  return (
    <div>
      <header className="app-main__header">
        <h1>Investor</h1>
        <p>
          Connect a wallet to view its balance and compliance status, and submit
          transactions.
        </p>
        {/*
          Disclosed up front because the issuer holds powers a plain ERC-20
          holder would not expect. This example app cannot state anyone's terms,
          so it describes what the contract allows and points to the issuer for
          the conditions.
        */}
        <p className="field__hint">
          This is a permissioned token. The issuer can pause ordinary transfers,
          freeze part or all of a holder&apos;s balance, and move tokens without
          the holder&apos;s signature for compliance or legal obligations. Each
          action is recorded on-chain. Ask the issuer when these powers may be
          used.
        </p>
      </header>

      <LegalDisclosure
        legal={project.status === "success" ? project.data.legal : undefined}
      />

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
        frozenTokens={
          ownWalletStatus.status === "success"
            ? (ownWalletStatus.data?.frozenTokens ?? undefined)
            : undefined
        }
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
 * The connected wallet's own compliance status, read through the
 * X-Wallet-Session minted at challenge-verify, not the admin-only wallet list.
 * `data` is `null` (a result, not an error) when `address` has no unexpired
 * session, i.e. the investor hasn't completed an ownership challenge or the
 * session has expired.
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
          // The server rejected a token that looked live locally; drop it so
          // the UI prompts a new challenge instead of retrying a dead token
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
 * In-memory list of the transactions this page has submitted from the
 * connected wallet. It complements the server's transaction index, which
 * lags the chain and never records approvals or plain transfers.
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
      setError(describeTxError(err, "Signing failed."));
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
      // Store the session and have the KYC section re-read its own status
      // through it
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
            <dd>{expectedChainId ?? "-"}</dd>
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
              Compliance status:{" "}
              {
                COMPLIANCE_STATUS_LABEL[
                  complianceDisplayStatus(verified.status, verified.validUntil)
                ]
              }
              .
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
            <dd>
              {
                COMPLIANCE_STATUS_LABEL[
                  complianceDisplayStatus(status.status, status.validUntil)
                ]
              }
            </dd>
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
          Sign an ownership challenge in the Wallet section to view your
          compliance status through a session scoped to your address.
        </p>
      )}
    </section>
  );
}

/** Parses a minimal-units string, treating anything unusable as zero. */
function safeBigInt(value: string | undefined): bigint {
  if (!value) return 0n;
  try {
    return BigInt(value);
  } catch {
    return 0n;
  }
}

function BalanceSection({
  project,
  address,
  chainMismatch,
  frozenTokens,
}: {
  project: Project | undefined;
  address: string | null | undefined;
  chainMismatch: boolean;
  /** This wallet's own frozen amount, in token minimal units, from GET /me/wallet-status. */
  frozenTokens: string | undefined;
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
  // The frozen amount can exceed the balance (RWAToken stores it as an
  // absolute amount), so the spendable figure is clamped at zero
  const frozen = safeBigInt(frozenTokens);
  const held = balance ? safeBigInt(balance.raw) : 0n;
  const spendable = held > frozen ? held - frozen : 0n;

  async function handleLoad() {
    if (!token || !address || !chainId) return;
    setLoading(true);
    setError(null);
    try {
      const tokenAddress = token as `0x${string}`;
      // Use Project.decimals; read it on-chain only if the response omits it
      const decimals =
        project?.decimals ?? (await readErc20Decimals(chainId, tokenAddress));
      const raw = await readErc20Balance(
        chainId,
        tokenAddress,
        address as `0x${string}`,
      );
      setBalance({ raw, decimals });
    } catch (err) {
      setError(describeTxError(err, "Failed to read balance."));
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
            <>
              <p>
                {formatWithDecimals(balance.raw, balance.decimals)} {unit}
              </p>
              {frozen > 0n && (
                <p className="field__hint" role="status">
                  {formatWithDecimals(spendable.toString(), balance.decimals)}{" "}
                  {unit} of that is available to send or redeem. The issuer has
                  frozen the other{" "}
                  {formatWithDecimals(frozenTokens ?? "0", balance.decimals)}{" "}
                  {unit}. Frozen tokens stay in your wallet. Transfers and
                  redemptions cannot spend them. Contact the issuer about an
                  unexpected hold.
                </p>
              )}
            </>
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
  // Decimals captured at preview time so the preview shows the minimal-unit
  // amounts as whole units: RWA decimals for the token amount, quote-token
  // decimals for the quote amounts
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
  // On buy the Vault pulls the quote token, not the RWA token
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
      // Whole units to RWA minimal units. toMinimalUnits throws an
      // AmountFormatError on invalid or over-precise input
      const rwaDecimals = await resolveRwaDecimals(project, chainId);
      const minimalTokenAmount = toMinimalUnits(tokenAmount, rwaDecimals);
      // The price comes from the Vault, read through the connected wallet: the
      // contract the buy is sent to, on the same chain
      const quoteAmount = await readPreviewBuy(
        chainId,
        vault as `0x${string}`,
        BigInt(minimalTokenAmount),
      );
      setDecimals(rwaDecimals);
      // Quote-token decimals are display-only: if the read fails the quote
      // still stands and the preview shows the digit-grouped raw integer
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
      setError(describeTxError(err, "Quote request failed."));
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
      // Wait for the approval to be mined, then read the on-chain allowance;
      // Buy stays disabled until it is sufficient
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
      setError(describeTxError(err, "Approval failed."));
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
      // Encoded here and sent to this project's Vault. The buyer is the
      // recipient, and the quote is capped by the slippage ceiling
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
      setError(describeTxError(err, "Buy failed."));
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
          Capped at {MAX_SLIPPAGE_BPS / 100}%, so the approved spend is never
          more than that far above the quote.
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
  // The entered RWA amount in minimal units, fixed at preview time so the
  // quote, approval and request all use the same value
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

  // The connected wallet's redemption requests: the public listRedemptions
  // endpoint filtered by beneficiary. It is queried only while a wallet is
  // connected
  const myRedemptions = usePaginatedList<Redemption>(
    (cursor, signal) =>
      walletAddress
        ? api.listRedemptions({ address: walletAddress, cursor, signal })
        : Promise.resolve({ items: [] }),
    [walletAddress],
  );

  // Display decimals for the list rows: RWA decimals from the project, and
  // quote-token decimals from Project.quoteDecimals or an on-chain read. If
  // neither is available the row shows the digit-grouped raw integer instead of
  // a mis-scaled value
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
    // A new quote invalidates the previous approval, whose allowance covers
    // the old amount
    setApproveTx(null);
    setAllowance(null);
    try {
      if (!escrow || !chainId) throw new Error("Project addresses not loaded.");
      // Whole units to RWA minimal units before the approve and request calls
      const rwaDecimals = await resolveRwaDecimals(project, chainId);
      const minimal = toMinimalUnits(rwaAmount, rwaDecimals);
      // Priced by the escrow the request is sent to, read through the wallet
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
      setError(describeTxError(err, "Quote request failed."));
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
      // Wait for the approval to be mined, then read the on-chain allowance;
      // Request stays disabled until it is sufficient
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
      setError(describeTxError(err, "Approval failed."));
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
      setError(describeTxError(err, "Redemption request failed."));
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
      setError(describeTxError(err, "Claim failed."));
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
      setError(describeTxError(err, "Cancel failed."));
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
          max={MAX_SLIPPAGE_BPS}
          value={slippageBps}
          onChange={(e) => setSlippageBps(Number(e.target.value))}
          aria-describedby="redeem-slippage-hint"
        />
        <span className="field__hint" id="redeem-slippage-hint">
          Capped at {MAX_SLIPPAGE_BPS / 100}%, so the minimum payout accepted is
          never more than that far below the quote.
        </span>
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
                      // Claim shows when the server's `claimable` is true
                      // (Funded and enough confirmations). Cancel shows for a
                      // Pending request whose timeout has elapsed. Neither
                      // shows for Completed, Rejected or Cancelled requests,
                      // or for a Funded one that isn't claimable yet.
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

  // Public, unauthenticated eligibility preflight. AllowedResult is only a
  // boolean, not the recipient's status, validUntil or ownership, so a caller
  // can't read a third party's compliance record from it
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
      // Whole units to RWA minimal units before encoding the ERC-20 transfer
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
      setError(describeTxError(err, "Transfer failed."));
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
            : "Recipient is not eligible to receive tokens - transfer disabled."}
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
 * Wallet activity has two views: the server's address-filtered transaction index
 * with confirmation and reorg status, and this page's submissions without tracked
 * confirmation. The index can lag and excludes approvals and plain transfers.
 */
function HistorySection({
  entries,
  walletAddress,
}: {
  entries: SubmittedTx[];
  walletAddress: string | null | undefined;
}) {
  // Server-indexed transactions involving this wallet (public, paginated),
  // queried only while a wallet is connected
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
          Connect a wallet to view its transactions from the server&apos;s chain
          index.
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
                        <td>{tx.kind ?? "-"}</td>
                        <td>
                          {tx.status && (
                            <TransactionStatusBadge status={tx.status} />
                          )}
                        </td>
                        <td>{tx.blockNumber ?? "-"}</td>
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
          No transactions sent from this wallet during this session. Submit one
          above to see it here.
        </p>
      ) : (
        <>
          <p className="field__hint">
            Transactions sent from your wallet during this session. The
            server&apos;s index above may take time to include them.
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
