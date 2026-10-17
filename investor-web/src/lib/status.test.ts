import { describe, expect, it } from "vitest";
import {
  complianceDisplayStatus,
  needsAttention,
  redemptionBlockedByCompliance,
  redemptionDisplayStatus,
  transactionStatusLabel,
} from "./status";

describe("redemptionDisplayStatus", () => {
  it.each([
    ["Pending", "Pending"],
    ["Completed", "Completed"],
    ["Rejected", "Rejected"],
    ["Cancelled", "Cancelled"],
    [undefined, "Unknown"],
    ["Mystery", "Unknown"],
  ])("maps %s to %s", (status, expected) => {
    expect(redemptionDisplayStatus(status as never)).toBe(expected);
  });

  it("trusts the server claimable flag over the confirmation count", () => {
    expect(
      redemptionDisplayStatus("Funded", { claimable: true, confirmations: 0 }),
    ).toBe("Claimable");
    expect(
      redemptionDisplayStatus("Funded", {
        claimable: false,
        confirmations: 99,
      }),
    ).toBe("Funded");
  });

  it("falls back to confirmations against the project threshold", () => {
    expect(
      redemptionDisplayStatus("Funded", {
        confirmations: 5,
        finalityConfirmations: 6,
      }),
    ).toBe("Funded");
    expect(
      redemptionDisplayStatus("Funded", {
        confirmations: 6,
        finalityConfirmations: 6,
      }),
    ).toBe("Claimable");
  });

  it("falls back to 12 confirmations when no threshold is supplied", () => {
    expect(redemptionDisplayStatus("Funded", { confirmations: 11 })).toBe(
      "Funded",
    );
    expect(redemptionDisplayStatus("Funded", { confirmations: 12 })).toBe(
      "Claimable",
    );
    expect(redemptionDisplayStatus("Funded")).toBe("Funded");
  });
});

describe("transaction status", () => {
  it("labels a known status and falls back to Unknown for others", () => {
    expect(transactionStatusLabel("confirmed")).toBe("Confirmed");
    expect(transactionStatusLabel("toString")).toBe("Unknown");
    expect(transactionStatusLabel("brand_new")).toBe("Unknown");
  });

  it.each([
    "reverted",
    "reorged",
    "failed",
    "broadcast_unknown",
    "nonce_consumed_externally",
    "needs_intervention",
    "brand_new",
  ])("flags %s for attention", (status) =>
    expect(needsAttention(status)).toBe(true),
  );

  it.each(["pending", "mined", "confirmed", "replaced"])(
    "does not flag %s",
    (status) => expect(needsAttention(status)).toBe(false),
  );
});

describe("redemptionBlockedByCompliance", () => {
  it("blocks only a Pending request whose beneficiary is explicitly not allowed", () => {
    expect(redemptionBlockedByCompliance("Pending", false)).toBe(true);
    expect(redemptionBlockedByCompliance("Pending", true)).toBe(false);
    expect(redemptionBlockedByCompliance("Pending", undefined)).toBe(false);
    expect(redemptionBlockedByCompliance("Funded", false)).toBe(false);
  });
});

describe("complianceDisplayStatus", () => {
  const NOW = 1_000_000;
  it("shows Expired only when a nonzero validUntil is strictly in the past", () => {
    expect(complianceDisplayStatus("Allowed", NOW - 1, NOW)).toBe("Expired");
    expect(complianceDisplayStatus("Allowed", NOW, NOW)).toBe("Allowed");
    expect(complianceDisplayStatus("Allowed", NOW + 1, NOW)).toBe("Allowed");
    expect(complianceDisplayStatus("Allowed", 0, NOW)).toBe("Allowed");
    expect(complianceDisplayStatus("Allowed", undefined, NOW)).toBe("Allowed");
  });

  it("maps Blocked and every other status", () => {
    expect(complianceDisplayStatus("Blocked", NOW + 1, NOW)).toBe("Blocked");
    expect(complianceDisplayStatus(undefined, undefined, NOW)).toBe(
      "NotOnboarded",
    );
    expect(complianceDisplayStatus("None" as never, undefined, NOW)).toBe(
      "NotOnboarded",
    );
  });
});
