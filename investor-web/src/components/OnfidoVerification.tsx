import { useEffect, useRef } from "react";
import { Onfido } from "onfido-sdk-ui";
import type { Handle } from "onfido-sdk-ui";

interface Props {
  /** SDK token from POST /api/v1/compliance/kyc/start. */
  token: string;
  /** The `ref` from the same response, used as the Onfido Studio workflow run id. */
  workflowRunId: string;
  /** The applicant finished the workflow; the outcome now arrives by webhook. */
  onSubmitted: () => void;
  onError: (message: string) => void;
}

/**
 * Onfido's web SDK in Studio workflow mode, mounted into a container this
 * component owns.
 *
 * As with Sumsub, no outcome is decided here: Onfido reports the result to the
 * server's webhook and the server sets the wallet to Allowed on-chain.
 * `onSubmitted` only moves this browser to a "waiting for approval" state.
 *
 * `onfido-sdk-ui` is a thin loader: `init()` returns a handle immediately and
 * imports the real SDK from Onfido's host in the background. The handle's
 * `tearDown()` is safe to call before that import finishes; it is queued until
 * the SDK loads.
 */
export function OnfidoVerification({
  token,
  workflowRunId,
  onSubmitted,
  onError,
}: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  // Held in refs so a parent re-render that changes a callback's identity
  // doesn't re-run the effect and restart the applicant's workflow. Only a new
  // token or workflowRunId, which identifies a different verification, may
  const onSubmittedRef = useRef(onSubmitted);
  onSubmittedRef.current = onSubmitted;
  const onErrorRef = useRef(onError);
  onErrorRef.current = onError;

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    let handle: Handle | undefined;
    try {
      handle = Onfido.init({
        token,
        workflowRunId,
        containerEl: container,
        onComplete: () => onSubmittedRef.current(),
        onError: (error) =>
          onErrorRef.current(error.message || "Onfido verification failed."),
      });
    } catch (err) {
      onErrorRef.current(
        err instanceof Error ? err.message : "Failed to start Onfido.",
      );
    }

    return () => {
      void handle?.tearDown();
    };
  }, [token, workflowRunId]);

  return <div className="kyc-sdk" ref={containerRef} />;
}
