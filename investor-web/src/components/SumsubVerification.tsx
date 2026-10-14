import SumsubWebSdk from "@sumsub/websdk-react";
import type {
  AnyEventName,
  AnyEventPayload,
  ErrorHandler,
} from "@sumsub/websdk";

type SnsError = Parameters<ErrorHandler>[0];

/**
 * Events that mean the applicant has handed their documents to Sumsub and the
 * widget has nothing further to ask. There is no single "done" event:
 * `onApplicantSubmitted` fires on a first submission, `onApplicantResubmitted`
 * on a re-submission, and the review-complete events when the review finishes
 * while the widget is still open.
 */
const SUBMITTED_EVENTS: ReadonlySet<string> = new Set([
  "idCheck.onApplicantSubmitted",
  "idCheck.onApplicantResubmitted",
  "idCheck.applicantReviewComplete",
  "idCheck.onApplicantReviewComplete",
  "idCheck.onApplicantVerificationCompleted",
]);

/**
 * Treats any `reviewStatus` other than `init` as submitted. `init` means the
 * applicant is still filling in the form; the other values (pending, queued,
 * prechecked, completed, onHold) mean Sumsub already has a submission. This
 * covers a returning applicant whose status event arrives without a submit
 * event.
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
   * new token (another /kyc/start round trip).
   */
  onTokenExpired: () => Promise<string>;
  /** The applicant is done with the widget; the outcome now arrives by webhook. */
  onSubmitted: () => void;
  onError: (message: string) => void;
}

/**
 * Sumsub's React widget, mounted with a server-issued access token.
 *
 * Nothing here decides the compliance outcome: the applicant's documents go
 * straight to Sumsub, Sumsub calls the server's webhook, and the server sets
 * the wallet to Allowed on-chain. `onSubmitted` only moves this browser to a
 * "waiting for approval" state.
 *
 * The underlying component tears the widget down in componentWillUnmount, so
 * unmounting this one cleans up.
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
