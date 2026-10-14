import { useState, type ReactNode } from "react";

interface ConfirmStepProps {
  /** Label for the button that opens the confirm panel */
  reviewLabel: string;
  /** Label for the button in the confirm panel that submits */
  confirmLabel: string;
  confirmHeading: string;
  /** Why the action needs a second look, such as "This calls the server's compliance hot key directly" */
  confirmWarning: string;
  /** Disables the review button, for example while required fields are empty */
  disabled?: boolean;
  submitting?: boolean;
  onConfirm: () => void;
  /** Summary of the action, rendered inside the confirm panel */
  children: ReactNode;
}

/** Two-step review and confirm gate for admin actions that call a server hot-key endpoint directly */
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
