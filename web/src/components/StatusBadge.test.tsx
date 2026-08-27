import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  RedemptionComplianceBadge,
  RedemptionStatusBadge,
  TransactionStatusBadge,
} from "./StatusBadge";
import { FALLBACK_FINALITY_CONFIRMATIONS, needsAttention } from "../lib/status";

describe("RedemptionStatusBadge", () => {
  it("shows Pending as-is", () => {
    render(<RedemptionStatusBadge status="Pending" />);
    expect(screen.getByText("Pending")).toBeInTheDocument();
  });

  it("shows Funded as still confirming below the finality threshold", () => {
    render(<RedemptionStatusBadge status="Funded" confirmations={1} />);
    const badge = screen.getByText(/Funded/);
    expect(badge).toBeInTheDocument();
    expect(badge.closest(".badge")).toHaveAttribute("data-status", "Funded");
  });

  it("promotes Funded to Claimable once past the finality threshold (fallback)", () => {
    render(
      <RedemptionStatusBadge
        status="Funded"
        confirmations={FALLBACK_FINALITY_CONFIRMATIONS}
      />,
    );
    const badge = screen.getByText("Claimable");
    expect(badge.closest(".badge")).toHaveAttribute("data-status", "Claimable");
  });

  it("trusts the server-derived claimable flag over the confirmations fallback", () => {
    render(
      <RedemptionStatusBadge status="Funded" claimable confirmations={0} />,
    );
    expect(screen.getByText("Claimable")).toBeInTheDocument();
  });

  it("never renders Funded/Claimable copy as a guarantee", () => {
    render(<RedemptionStatusBadge status="Funded" claimable />);
    expect(screen.queryByText(/guarantee/i)).not.toBeInTheDocument();
  });
});

describe("RedemptionComplianceBadge", () => {
  it("renders a blocked indicator for a Pending request whose beneficiary is not Allowed", () => {
    render(
      <RedemptionComplianceBadge status="Pending" beneficiaryAllowed={false} />,
    );
    expect(screen.getByText("Blocked by compliance")).toBeInTheDocument();
  });

  it("renders nothing when the beneficiary is Allowed", () => {
    const { container } = render(
      <RedemptionComplianceBadge status="Pending" beneficiaryAllowed={true} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing once the request is Funded, even if the beneficiary is not Allowed (already committed)", () => {
    const { container } = render(
      <RedemptionComplianceBadge status="Funded" beneficiaryAllowed={false} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("degrades gracefully when beneficiaryAllowed is missing from the response", () => {
    const { container } = render(
      <RedemptionComplianceBadge status="Pending" />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});

describe("TransactionStatusBadge", () => {
  it("labels a pending tx as unconfirmed", () => {
    render(<TransactionStatusBadge status="pending" />);
    expect(screen.getByText("Unconfirmed")).toBeInTheDocument();
  });

  it("labels a reorged tx distinctly from reverted, with a resubmission hint", () => {
    render(<TransactionStatusBadge status="reorged" />);
    const badge = screen.getByText(/Reorged/);
    expect(badge).toHaveTextContent("resubmission required");
    expect(badge.closest(".badge")).toHaveAttribute("data-status", "reorged");
  });

  it("labels a reverted tx as a definite on-chain failure", () => {
    render(<TransactionStatusBadge status="reverted" />);
    expect(screen.getByText(/Reverted/)).toHaveTextContent("failed on-chain");
  });

  // Exhaustive coverage of every domain.TxStatus: label text, tone class,
  // data-status attribute, and whether the row is marked as needing attention.
  const CASES: Array<{
    status: string;
    label: string;
    tone: string;
    attention: boolean;
  }> = [
    {
      status: "pending",
      label: "Unconfirmed",
      tone: "badge--neutral",
      attention: false,
    },
    {
      status: "mined",
      label: "Mined (confirming)",
      tone: "badge--info",
      attention: false,
    },
    {
      status: "confirmed",
      label: "Confirmed",
      tone: "badge--success",
      attention: false,
    },
    {
      status: "replaced",
      label: "Replaced",
      tone: "badge--warning",
      attention: false,
    },
    {
      status: "reverted",
      label: "Reverted — failed on-chain",
      tone: "badge--danger",
      attention: true,
    },
    {
      status: "reorged",
      label: "Reorged — resubmission required",
      tone: "badge--warning",
      attention: true,
    },
    {
      status: "failed",
      label: "Failed — never broadcast",
      tone: "badge--danger",
      attention: true,
    },
    {
      status: "broadcast_unknown",
      label: "Broadcast unknown — do not resubmit",
      tone: "badge--warning",
      attention: true,
    },
    {
      status: "nonce_consumed_externally",
      label: "Nonce consumed externally",
      tone: "badge--warning",
      attention: true,
    },
    {
      status: "needs_intervention",
      label: "Needs operator intervention",
      tone: "badge--danger",
      attention: true,
    },
  ];

  it.each(CASES)(
    "renders $status with its label, tone, and attention marker",
    ({ status, label, tone, attention }) => {
      render(<TransactionStatusBadge status={status} />);
      const badge = screen.getByText(label);
      expect(badge).toHaveClass("badge", tone);
      expect(badge).toHaveAttribute("data-status", status);
      if (attention) {
        expect(badge).toHaveAttribute("data-attention", "true");
      } else {
        expect(badge).not.toHaveAttribute("data-attention");
      }
    },
  );

  it("renders a visible Unknown badge for a forward-compat status the client doesn't know", () => {
    render(<TransactionStatusBadge status="some_future_state" />);
    const badge = screen.getByText("Unknown");
    expect(badge).toHaveClass("badge", "badge--warning");
    expect(badge).toHaveAttribute("data-status", "unknown");
    // An unrecognized status fails closed toward operator visibility.
    expect(badge).toHaveAttribute("data-attention", "true");
    expect(badge).toHaveAttribute(
      "title",
      expect.stringContaining("some_future_state"),
    );
  });
});

describe("needsAttention", () => {
  it("flags every stopped/unsafe state and clears the self-progressing ones", () => {
    expect(needsAttention("pending")).toBe(false);
    expect(needsAttention("mined")).toBe(false);
    expect(needsAttention("confirmed")).toBe(false);
    expect(needsAttention("replaced")).toBe(false);
    expect(needsAttention("reverted")).toBe(true);
    expect(needsAttention("reorged")).toBe(true);
    expect(needsAttention("failed")).toBe(true);
    expect(needsAttention("broadcast_unknown")).toBe(true);
    expect(needsAttention("nonce_consumed_externally")).toBe(true);
    expect(needsAttention("needs_intervention")).toBe(true);
  });

  it("flags an unrecognized forward-compat status (fails closed)", () => {
    expect(needsAttention("brand_new_status")).toBe(true);
  });
});
