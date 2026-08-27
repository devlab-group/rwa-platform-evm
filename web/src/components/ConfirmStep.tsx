import { useState, type ReactNode } from "react";

interface ConfirmStepProps {
  /** Label for the initial button that reveals the confirm panel. */
  reviewLabel: string;
  /** Label for the button inside the confirm panel that actually submits. */
  confirmLabel: string;
  confirmHeading: string;
  /** Why this needs a second look — e.g. "This calls the server's compliance hot key directly." */
  confirmWarning: string;
  /** Disables the initial review button (e.g. required fields still empty). */
  disabled?: boolean;
  submitting?: boolean;
  onConfirm: () => void;
  /** Summary of what's about to happen — rendered inside the confirm panel. */
  children: ReactNode;
}

/**
 * Two-step confirm gate for admin actions that call a server hot-key
 * endpoint directly. Reuses the same review-then-confirm shape as
 * Setup's deploy flow.
 */
export function ConfirmStep({
  reviewLabel,
  confirmLabel,
  confirmHeading,
  confirmWarning,
  disabled,
  submitting,
  onConfirm,
  children,
}: ConfirmStepProps) {
  const [confirming, setConfirming] = useState(false);

  if (!confirming) {
    return (
      <button
        type="button"
        className="button button--primary"
        onClick={() => setConfirming(true)}
        disabled={disabled}
      >
        {reviewLabel}
      </button>
    );
  }

  return (
    <div className="tx-preview">
      <h3 className="tx-preview__title">{confirmHeading}</h3>
      <p>{confirmWarning}</p>
      {children}
      <div className="tx-preview__actions">
        <button
          type="button"
          className="button button--primary"
          onClick={onConfirm}
          disabled={submitting}
        >
          {submitting ? "Submitting…" : confirmLabel}
        </button>
        <button
          type="button"
          className="button button--secondary"
          onClick={() => setConfirming(false)}
          disabled={submitting}
        >
          Back
        </button>
      </div>
    </div>
  );
}
