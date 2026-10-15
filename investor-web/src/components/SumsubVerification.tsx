import SumsubWebSdk from "@sumsub/websdk-react";
import type {
  AnyEventName,
  AnyEventPayload,
  ErrorHandler,
} from "@sumsub/websdk";

type SnsError = Parameters<ErrorHandler>[0];

/**
 * Submission, resubmission and review-completion events move the widget to waiting;
 * the event names below cover the supported completion signals
 */
const SUBMITTED_EVENTS: ReadonlySet<string> = new Set([
  "idCheck.onApplicantSubmitted",
  "idCheck.onApplicantResubmitted",
  "idCheck.applicantReviewComplete",
  "idCheck.onApplicantReviewComplete",
  "idCheck.onApplicantVerificationCompleted",
]);

/**
 * Treats string reviewStatus values other than init as submitted, including pending,
 * queued, prechecked, completed and onHold, for returning applicants without a submit event
 */
function isSubmittedStatus(payload: AnyEventPayload): boolean {
  return (
    "reviewStatus" in payload &&
    typeof payload.reviewStatus === "string" &&
    payload.reviewStatus !== "init"
  );
}

interface Props {
  // Access token from POST /api/v1/compliance/kyc/start
  accessToken: string;
  // Must fetch a new token from /kyc/start when Sumsub reports expiry
  onTokenExpired: () => Promise<string>;
  // Reports submission; the decision arrives by webhook
  onSubmitted: () => void;
  onError: (message: string) => void;
}

/**
 * Mounts Sumsub with a server-issued token; onSubmitted starts the waiting state.
 * KycVerificationSection handles status polling after the server processes the webhook.
 * The SDK component tears down the widget on unmount.
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
