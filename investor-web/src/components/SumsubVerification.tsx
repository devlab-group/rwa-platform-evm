import SumsubWebSdk from "@sumsub/websdk-react";
import type {
  AnyEventName,
  AnyEventPayload,
  ErrorHandler,
} from "@sumsub/websdk";

type SnsError = Parameters<ErrorHandler>[0];

/**
 * Events that mean the applicant has handed their documents to Sumsub and the
 * widget has nothing further to ask them for. There is no single "done" event:
 * a first-time applicant emits `onApplicantSubmitted`, a re-submission emits
 * `onApplicantResubmitted`, and `applicantReviewComplete` arrives when the
 * review itself finishes while the widget is still open.
 */
const SUBMITTED_EVENTS: ReadonlySet<string> = new Set([
  "idCheck.onApplicantSubmitted",
  "idCheck.onApplicantResubmitted",
  "idCheck.applicantReviewComplete",
  "idCheck.onApplicantReviewComplete",
  "idCheck.onApplicantVerificationCompleted",
]);

/**
 * `init` is the only reviewStatus that means "still filling the form in". Every
 * other value (pending, queued, prechecked, completed, onHold…) means Sumsub
 * has the submission, so a returning applicant whose status event arrives
 * without a fresh submit event is still treated as submitted.
 */
function isSubmittedStatus(payload: AnyEventPayload): boolean {
  return (
    "reviewStatus" in payload &&
    typeof payload.reviewStatus === "string" &&
    payload.reviewStatus !== "init"
  );
}

interface Props {
  /** Access token from POST /api/v1/compliance/kyc/start. */
  accessToken: string;
  /**
   * Called when Sumsub reports the token expired mid-flow; must resolve to a
   * freshly minted one (i.e. another /kyc/start round trip) or the widget dies.
   */
  onTokenExpired: () => Promise<string>;
  /** The applicant is done with the widget; the outcome now arrives by webhook. */
  onSubmitted: () => void;
  onError: (message: string) => void;
}

/**
 * Sumsub's official React widget, mounted with a server-minted access token.
 *
 * Nothing here decides the compliance outcome: the applicant's documents go
 * straight to Sumsub, Sumsub calls the server's webhook, and the server flips
 * the wallet to Allowed on-chain. `onSubmitted` only moves this browser to a
 * "waiting for approval" state.
 *
 * The underlying component tears the widget down in componentWillUnmount, so
 * unmounting this one is enough to clean up.
 */
export function SumsubVerification({
  accessToken,
  onTokenExpired,
  onSubmitted,
  onError,
}: Props) {
  function handleMessage(type: AnyEventName, payload: AnyEventPayload): void {
    if (SUBMITTED_EVENTS.has(type) || isSubmittedStatus(payload)) onSubmitted();
  }

  function handleError(error: SnsError): void {
    onError(error.reason ?? error.error ?? "Sumsub verification failed.");
  }

  return (
    <div className="kyc-sdk">
      <SumsubWebSdk
        accessToken={accessToken}
        expirationHandler={onTokenExpired}
        onMessage={handleMessage}
        onError={handleError}
      />
    </div>
  );
}
