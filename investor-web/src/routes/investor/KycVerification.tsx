import { useCallback, useEffect, useRef, useState } from "react";
import { OnfidoVerification } from "../../components/OnfidoVerification";
import { SumsubVerification } from "../../components/SumsubVerification";
import { api, ApiError } from "../../lib/client";
import type { components } from "../../lib/api-types";
import { clearWalletSession, getWalletSession } from "../../lib/walletSession";

type KYCSession = components["schemas"]["KYCSession"];
type WalletStatus = components["schemas"]["WalletStatus"];

/** How often the wallet's own status is re-read while waiting for approval. */
const POLL_INTERVAL_MS = 5_000;

/**
 * How long to keep polling before telling the applicant to come back later.
 * Provider review is frequently instant but can take hours; a browser tab left
 * open is not a reasonable place to wait for that, and the status is equally
 * visible on the next visit.
 */
const POLL_TIMEOUT_MS = 3 * 60_000;

const NOT_ENABLED_MESSAGE =
  "KYC verification is not enabled on this deployment.";

/**
 * The applicant's progress through one verification attempt. `submitted` means
 * the provider has the documents — from there the outcome arrives out of band
 * (provider → server webhook → on-chain status), so all this page can do is
 * watch its own wallet status.
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
   * The connected wallet's own status, as loaded by the page through its
   * X-Wallet-Session. `data === null` means there is no live session yet, which
   * is also the signal that ownership hasn't been proven. `reload` is what the
   * post-submission poll drives, so the KYC-status card above stays in sync
   * with this one instead of each fetching separately.
   */
  ownWalletStatus: {
    status: "loading" | "error" | "success";
    data?: WalletStatus | null;
    reload: () => void;
  };
}

/**
 * Identity verification: starts a provider session for the connected wallet
 * and mounts that provider's official web SDK.
 *
 * Nothing about the outcome is decided in the browser. The applicant's
 * documents go straight to the provider; the provider notifies the server over
 * its signed webhook; the server flips the wallet to Allowed on-chain. So once
 * the SDK signals it's done, the only thing left to do here is poll this
 * wallet's own status until the change shows up.
 */
export function KycVerificationSection({
  walletAddress,
  ownWalletStatus,
}: Props) {
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });

  // A live X-Wallet-Session is the precondition for everything below: the
  // subject wallet is taken from it server-side, so there's no way to start a
  // verification without one.
  const hasSession =
    ownWalletStatus.status === "success" && ownWalletStatus.data != null;
  const status = ownWalletStatus.data?.status;
  const decided = status === "Allowed" || status === "Blocked";

  // Reset when the connected wallet changes — a session and a verification both
  // belong to exactly one address.
  useEffect(() => {
    setPhase({ kind: "idle" });
  }, [walletAddress]);

  /**
   * One /kyc/start round trip. Also used as Sumsub's expiration handler, which
   * is why it returns the session rather than storing it: an expired token is
   * replaced mid-flow without disturbing the mounted widget.
   */
  const start = useCallback(async (): Promise<KYCSession> => {
    if (!walletAddress) throw new Error("Connect a wallet first.");
    const session = getWalletSession(walletAddress);
    if (!session) {
      throw new Error(
        "Prove wallet ownership in the Wallet section first — the verification is bound to your verified session.",
      );
    }
    try {
      return await api.startKYC(session.token);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        // The token looked live locally but the server has already dropped it;
        // clear it so the page prompts a fresh challenge.
        clearWalletSession(walletAddress);
        ownWalletStatus.reload();
      }
      throw err;
    }
    // ownWalletStatus.reload is a fresh closure each render but always does the
    // same thing; depending on it would rebuild this callback continuously.
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
          Generate and sign an ownership challenge in the Wallet section first.
          Verification is started for the address you proved you control, not
          one you type in.
        </p>
      ) : (
        <>
          {status === "Allowed" ? (
            <p className="field__hint" role="status">
              This wallet is already Allowed — no further verification needed.
            </p>
          ) : (
            <p className="field__hint">
              Your documents go directly to the verification provider. Approval
              is applied to your wallet automatically once the provider reports
              the result — you won&apos;t need to submit anything on-chain.
            </p>
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
              {phase.message} Verification for this deployment is arranged
              directly with the issuer; contact them to be approved.
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
 * Mounts the SDK the server named. `generic` never reaches here (it's turned
 * into the unavailable phase at start), and a provider that came back without
 * the credentials its SDK needs is reported rather than mounted half-configured.
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
          The provider session is missing its access token — verification cannot
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
          The provider session is missing its token or workflow run —
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

  // A provider a newer server knows about and this build doesn't. The hosted
  // URL is the documented fallback for exactly that case.
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
      This build doesn&apos;t know how to display the configured verification
      provider. Contact the issuer.
    </p>
  );
}

/** What the applicant sees between "documents sent" and an on-chain decision. */
function ReviewProgress({
  status,
  timedOut,
  onRefresh,
}: {
  status: WalletStatus["status"];
  timedOut: boolean;
  onRefresh: () => void;
}) {
  if (status === "Allowed") {
    return (
      <p className="field__hint" role="status">
        Approved — your wallet is now Allowed and can buy, transfer and redeem.
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
        Verification submitted — awaiting approval.{" "}
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
 * Re-reads the wallet's own status every few seconds while `active`, giving up
 * after POLL_TIMEOUT_MS. Returns whether it gave up, so the caller can offer a
 * manual re-check instead of polling a review that may take hours.
 */
function useStatusPoll(active: boolean, refresh: () => void): boolean {
  const [timedOut, setTimedOut] = useState(false);
  // `refresh` is a new closure every render; a ref keeps the interval from
  // being torn down and restarted on each one.
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
