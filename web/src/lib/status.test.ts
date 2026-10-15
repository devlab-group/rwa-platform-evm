import { describe, expect, it } from "vitest";
import { complianceDisplayStatus } from "./status";

describe("complianceDisplayStatus", () => {
  const now = 1_000;

  it("matches the registry's isAllowed expiry rule", () => {
    expect(complianceDisplayStatus("Allowed", undefined, now)).toBe("Allowed");
    expect(complianceDisplayStatus("Allowed", 0, now)).toBe("Allowed");
    // The registry allows a record at its exact expiry timestamp
    expect(complianceDisplayStatus("Allowed", now, now)).toBe("Allowed");
    expect(complianceDisplayStatus("Allowed", now - 1, now)).toBe("Expired");
  });

  it("ignores expiry for Blocked and unknown wallets", () => {
    expect(complianceDisplayStatus("Blocked", now - 1, now)).toBe("Blocked");
    expect(complianceDisplayStatus("Unknown", now - 1, now)).toBe(
      "NotOnboarded",
    );
    expect(complianceDisplayStatus(undefined, undefined, now)).toBe(
      "NotOnboarded",
    );
  });
});
