import { useState, type ReactNode } from "react";

interface ConfirmStepProps {
  /** Label for the button that opens the confirm panel */
  reviewLabel: string;
  /** Label for the button in the confirm panel that submits */
  confirmLabel: string;
  confirmHeading: string;
  // Reason for review, such as a server compliance-key call
  confirmWarning: string;
  // Disables review while required input is missing
  disabled?: boolean;
  submitting?: boolean;
  onConfirm: () => void;
  /** Summary of the action, rendered inside the confirm panel */
  children: ReactNode;
}

/** Review/confirm step for admin calls to server hot-key endpoints */
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
