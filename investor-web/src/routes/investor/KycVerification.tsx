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

/** How often the wallet's own status is re-read while waiting for approval. */
const POLL_INTERVAL_MS = 5_000;

/**
 * How long to keep polling before telling the applicant to come back later.
 * The provider's review time is outside this app's control, and the status is
 * equally visible on the next visit.
 */
const POLL_TIMEOUT_MS = 3 * 60_000;

const NOT_ENABLED_MESSAGE =
  "KYC verification is not enabled on this deployment.";

/**
 * The applicant's progress through one verification attempt. `submitted` means
 * the provider has the documents. The outcome then arrives out of band
 * (provider, server webhook, on-chain status), so this page can only watch its
 * own wallet status.
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
  /**
   * The connected wallet's own status, loaded by the page through its
   * X-Wallet-Session. `data === null` means there is no live session yet, so
   * ownership has not been proven. The post-submission poll calls `reload`,
   * which keeps the KYC status card in sync with this one.
   */
  ownWalletStatus: {
    status: "loading" | "error" | "success";
    data?: WalletStatus | null;
    reload: () => void;
  };
}

/**
 * Starts verification for the session's wallet and mounts the provider SDK.
 * Documents go to the provider; its signed webhook lets the server apply
 * approval on-chain. After SDK completion, poll the wallet's own status.
 */
export function KycVerificationSection({
  walletAddress,
  ownWalletStatus,
}: Props) {
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });

  // Everything below needs a live X-Wallet-Session: the server takes the
  // wallet from it, so a verification can't be started without one
  const hasSession =
    ownWalletStatus.status === "success" && ownWalletStatus.data != null;
  const status = complianceDisplayStatus(
    ownWalletStatus.data?.status,
    ownWalletStatus.data?.validUntil,
  );
  const decided = status === "Allowed" || status === "Blocked";

  // Reset when the connected wallet changes: a session and a verification each
  // belong to one address
  useEffect(() => {
    setPhase({ kind: "idle" });
  }, [walletAddress]);

  /**
   * One /kyc/start round trip. It returns the session instead of storing it
   * because Sumsub's expiration handler also uses it: an expired token is
   * replaced mid-flow without remounting the widget.
   */
  const start = useCallback(async (): Promise<KYCSession> => {
    if (!walletAddress) throw new Error("Connect a wallet first.");
    const session = getWalletSession(walletAddress);
    if (!session) {
      throw new Error(
        "Prove wallet ownership in the Wallet section first - the verification is bound to your verified session.",
      );
    }
    try {
      return await api.startKYC(session.token);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        // The token looked live locally but the server rejected it; clear it so
        // the page prompts a new challenge
        clearWalletSession(walletAddress);
        ownWalletStatus.reload();
      }
      throw err;
    }
    // ownWalletStatus.reload is a new closure every render; depending on it
    // would rebuild this callback on each one
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

  // Poll only while there is an outcome to wait for.
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
          uses the address you proved you control.
        </p>
      ) : (
        <>
          {status === "Allowed" ? (
            <p className="field__hint" role="status">
              This wallet is already Allowed - no further verification needed.
            </p>
          ) : (
            <>
              {status === "Expired" && (
                <p className="async-state--error" role="status">
                  Your verification has expired. To buy, transfer or redeem with
                  this wallet, verify again and wait for approval.
                </p>
              )}
              <p className="field__hint">
                Your documents go directly to the provider. After approval, wait
                for your wallet&apos;s status to update; the server submits the
                on-chain change.
              </p>
            </>
          )}

          {(phase.kind === "idle" ||
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

/**
 * Mounts the SDK for the provider the server named. `generic` never reaches
 * here (handleStart turns it into the unavailable phase), and a session missing
 * the credentials its SDK needs shows an error instead of mounting the SDK.
 */
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
          The provider session is missing its access token - verification cannot
          start.
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
          The provider session is missing its token or workflow run -
          verification cannot start.
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

  // A provider this build has no SDK for: use the hosted-flow URL if the
  // server sent one
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
      This build cannot display the configured verification provider. Contact
      the issuer.
    </p>
  );
}

/** What the applicant sees between documents sent and an on-chain decision. */
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
        Approved - your wallet is now Allowed and can buy, transfer and redeem.
      </p>
    );
  }
  if (status === "Blocked") {
    return (
      <p className="async-state--error" role="alert">
        Your verification was not approved. Contact the issuer if you believe
        this is wrong.
      </p>
    );
  }
  return (
    <div className="u-mt-4">
      <p role="status">
        Verification submitted - awaiting approval.{" "}
        {timedOut
          ? "Still under review; it's safe to close this page and check back later."
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

/**
 * Re-reads the wallet's own status every POLL_INTERVAL_MS while `active`, giving
 * up after POLL_TIMEOUT_MS. Returns whether it gave up, so the caller can offer
 * a manual re-check.
 */
function useStatusPoll(active: boolean, refresh: () => void): boolean {
  const [timedOut, setTimedOut] = useState(false);
  // `refresh` is a new closure every render; a ref keeps the interval from
  // restarting on each one
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
