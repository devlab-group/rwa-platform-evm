import { useCallback, useEffect, useRef, useState } from "react";
import { OnfidoVerification } from "../../components/OnfidoVerification";
import { SumsubVerification } from "../../components/SumsubVerification";
import { api, ApiError } from "../../lib/client";
import type { components } from "../../lib/api-types";
import {
  complianceDisplayStatus,
  type ComplianceDisplayStatus,
} from "../../lib/status";
import { clearWalletSession, getWalletSession } from "../../lib/walletSession";

type KYCSession = components["schemas"]["KYCSession"];
type WalletStatus = components["schemas"]["WalletStatus"];

// Own-status polling interval while awaiting approval
const POLL_INTERVAL_MS = 5_000;

// Bound polling time; provider review may continue after the page closes
const POLL_TIMEOUT_MS = 3 * 60_000;

const NOT_ENABLED_MESSAGE =
  "KYC verification is not enabled on this deployment.";

/**
 * Verification progress: submitted means documents reached the provider;
 * observe wallet status for the webhook/on-chain decision
 */
type Phase =
  | { kind: "idle" }
  | { kind: "starting" }
  | { kind: "unavailable"; message: string }
  | { kind: "running"; session: KYCSession }
  | { kind: "submitted" }
  | { kind: "error"; message: string };

interface Props {
  walletAddress: string | null | undefined;
  // Own-wallet session status; null means no live session, reload synchronizes polling and KYC card
  ownWalletStatus: {
    status: "loading" | "error" | "success";
    data?: WalletStatus | null;
    reload: () => void;
  };
}

/**
 * Starts KYC for the session wallet and mounts its SDK; documents go to the provider.
 * Poll own status after submission while the server processes the signed webhook.
 */
export function KycVerificationSection({
  walletAddress,
  ownWalletStatus,
}: Props) {
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });

  // KYC start requires X-Wallet-Session; the server takes its wallet address from it
  const hasSession =
    ownWalletStatus.status === "success" && ownWalletStatus.data != null;
  const status = complianceDisplayStatus(
    ownWalletStatus.data?.status,
    ownWalletStatus.data?.validUntil,
  );
  const decided = status === "Allowed" || status === "Blocked";

  // Reset per-address verification state on wallet changes
  useEffect(() => {
    setPhase({ kind: "idle" });
  }, [walletAddress]);

  // Returns a /kyc/start session for initial launch or Sumsub token renewal without remounting
  const start = useCallback(async (): Promise<KYCSession> => {
    if (!walletAddress) throw new Error("Connect a wallet first.");
    const session = getWalletSession(walletAddress);
    if (!session) {
      throw new Error(
        "Prove wallet ownership in the Wallet section first; verification uses that session.",
      );
    }
    try {
      return await api.startKYC(session.token);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        // Clear rejected tokens and reload so the page prompts a fresh challenge
        clearWalletSession(walletAddress);
        ownWalletStatus.reload();
      }
      throw err;
    }
    // Exclude unstable reload closures to keep the callback stable
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [walletAddress]);

  async function handleStart() {
    setPhase({ kind: "starting" });
    try {
      const session = await start();
      setPhase(
        session.provider === "generic"
          ? { kind: "unavailable", message: NOT_ENABLED_MESSAGE }
          : { kind: "running", session },
      );
    } catch (err) {
      if (err instanceof ApiError && err.status === 501) {
        setPhase({ kind: "unavailable", message: NOT_ENABLED_MESSAGE });
        return;
      }
      setPhase({
        kind: "error",
        message:
          err instanceof Error ? err.message : "Failed to start verification.",
      });
    }
  }

  const refreshSumsubToken = useCallback(async () => {
    const session = await start();
    if (!session.token) throw new Error("Provider returned no access token.");
    return session.token;
  }, [start]);

  const handleSubmitted = useCallback(() => {
    setPhase((prev) =>
      prev.kind === "submitted" ? prev : { kind: "submitted" },
    );
  }, []);

  const handleSdkError = useCallback((message: string) => {
    setPhase({ kind: "error", message });
  }, []);

  // Poll while awaiting a decision
  const pollTimedOut = useStatusPoll(
    phase.kind === "submitted" && !decided,
    ownWalletStatus.reload,
  );

  return (
    <section className="card">
      <h2>Verify identity (KYC)</h2>

      {!walletAddress ? (
        <p className="field__hint">
          Connect a wallet to start identity verification.
        </p>
      ) : !hasSession ? (
        <p className="field__hint">
          Sign an ownership challenge in the Wallet section first. Verification
          uses that address.
        </p>
      ) : (
        <>
          {status === "Allowed" ? (
            <p className="field__hint" role="status">
              This wallet is already Allowed; no further verification is needed.
            </p>
          ) : status === "Blocked" ? (
            // After a submission, ReviewProgress reports the Blocked decision instead
            phase.kind !== "submitted" && (
              <p className="async-state--error" role="status">
                This wallet is Blocked. Contact the issuer to resolve it;
                verification cannot be restarted here.
              </p>
            )
          ) : (
            <>
              {status === "Expired" && (
                <p className="async-state--error" role="status">
                  Your verification expired. Verify again and await approval
                  before buying, transferring or requesting redemption.
                </p>
              )}
              <p className="field__hint">
                Documents go directly to the provider. After approval, the
                server submits the on-chain status change; wait for your wallet
                status to update.
              </p>
            </>
          )}

          {/* A current Allowed or Blocked decision leaves nothing to start */}
          {!decided &&
            (phase.kind === "idle" ||
              phase.kind === "starting" ||
              phase.kind === "error") && (
              <button
                type="button"
                className="button button--primary"
                onClick={handleStart}
                disabled={phase.kind === "starting"}
              >
                {phase.kind === "starting"
                  ? "Starting…"
                  : phase.kind === "error"
                    ? "Retry verification"
                    : "Start verification"}
              </button>
            )}

          {phase.kind === "error" && (
            <p className="async-state--error" role="alert">
              {phase.message}
            </p>
          )}

          {phase.kind === "unavailable" && (
            <p className="field__hint" role="status">
              {phase.message} Contact the issuer to arrange verification for
              this deployment.
            </p>
          )}

          {phase.kind === "running" && (
            <ProviderFlow
              session={phase.session}
              onTokenExpired={refreshSumsubToken}
              onSubmitted={handleSubmitted}
              onError={handleSdkError}
            />
          )}

          {phase.kind === "submitted" && (
            <ReviewProgress
              status={status}
              timedOut={pollTimedOut}
              onRefresh={ownWalletStatus.reload}
            />
          )}
        </>
      )}
    </section>
  );
}

// Mount the named SDK after validating its credentials; handleStart routes generic to unavailable
function ProviderFlow({
  session,
  onTokenExpired,
  onSubmitted,
  onError,
}: {
  session: KYCSession;
  onTokenExpired: () => Promise<string>;
  onSubmitted: () => void;
  onError: (message: string) => void;
}) {
  if (session.provider === "sumsub") {
    if (!session.token) {
      return (
        <p className="async-state--error" role="alert">
          The provider session has no access token; verification cannot start.
        </p>
      );
    }
    return (
      <SumsubVerification
        accessToken={session.token}
        onTokenExpired={onTokenExpired}
        onSubmitted={onSubmitted}
        onError={onError}
      />
    );
  }

  if (session.provider === "onfido") {
    if (!session.token || !session.ref) {
      return (
        <p className="async-state--error" role="alert">
          The provider session lacks a token or workflow run; verification
          cannot start.
        </p>
      );
    }
    return (
      <OnfidoVerification
        token={session.token}
        workflowRunId={session.ref}
        onSubmitted={onSubmitted}
        onError={onError}
      />
    );
  }

  // Use a hosted-flow URL for providers without a bundled SDK
  return session.url ? (
    <p className="field__hint">
      Continue your verification at{" "}
      <a href={session.url} target="_blank" rel="noopener noreferrer">
        the provider&apos;s hosted flow
      </a>
      .
    </p>
  ) : (
    <p className="async-state--error" role="alert">
      This build does not support the configured provider. Contact the issuer.
    </p>
  );
}

// Displays review progress until wallet status changes
function ReviewProgress({
  status,
  timedOut,
  onRefresh,
}: {
  status: ComplianceDisplayStatus;
  timedOut: boolean;
  onRefresh: () => void;
}) {
  if (status === "Allowed") {
    return (
      <p className="field__hint" role="status">
        Approved - your wallet is now Allowed.
      </p>
    );
  }
  if (status === "Blocked") {
    return (
      <p className="async-state--error" role="alert">
        Your wallet is Blocked. Contact the issuer if this is unexpected.
      </p>
    );
  }
  return (
    <div className="u-mt-4">
      <p role="status">
        Verification submitted - awaiting approval.{" "}
        {timedOut
          ? "Still awaiting a decision; you can close this page and check back later."
          : "Checking your wallet status…"}
      </p>
      {timedOut && (
        <button
          type="button"
          className="button button--secondary"
          onClick={onRefresh}
        >
          Check again
        </button>
      )}
    </div>
  );
}

// Poll own status while active until POLL_TIMEOUT_MS; return timeout state for manual re-check
function useStatusPoll(active: boolean, refresh: () => void): boolean {
  const [timedOut, setTimedOut] = useState(false);
  // Keep refresh in a ref so new closures do not restart the interval
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  useEffect(() => {
    if (!active) return;
    setTimedOut(false);
    const startedAt = Date.now();
    const timer = window.setInterval(() => {
      if (Date.now() - startedAt >= POLL_TIMEOUT_MS) {
        window.clearInterval(timer);
        setTimedOut(true);
        return;
      }
      refreshRef.current();
    }, POLL_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [active]);

  return timedOut;
}
