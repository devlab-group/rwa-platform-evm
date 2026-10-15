import { useEffect, useRef } from "react";
import { Onfido } from "onfido-sdk-ui";
import type { Handle } from "onfido-sdk-ui";

interface Props {
  // SDK token from POST /api/v1/compliance/kyc/start
  token: string;
  // The response ref, used as Onfido Studio workflowRunId
  workflowRunId: string;
  // Reports workflow submission; the decision arrives by webhook
  onSubmitted: () => void;
  onError: (message: string) => void;
}

/**
 * Mounts Onfido Studio in an owned container; onSubmitted starts the waiting state.
 * KycVerificationSection handles status polling after the server processes the webhook.
 * init() returns before loading the SDK; tearDown() queues cleanup if it is still loading.
 */
export function OnfidoVerification({
  token,
  workflowRunId,
  onSubmitted,
  onError,
}: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  // Refs prevent callback-identity changes from restarting the workflow;
  // only token or workflowRunId changes restart the effect
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
