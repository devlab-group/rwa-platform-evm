import { describe, expect, it } from "vitest";
import {
  buildBurnAttestation,
  parseBurnAmount,
  verifyBurnSignedResult,
} from "./burn";
import { BURN, PROJECT, VAULT, signedFor } from "../test/burnFixtures";

describe("verifyBurnSignedResult", () => {
  it("accepts matching auditor and digest fields and returns the attestation", () => {
    const att = verifyBurnSignedResult(signedFor(BURN), BURN, PROJECT);
    expect(att).toEqual(buildBurnAttestation(BURN, PROJECT));
    expect(att.amount).toBe(250000000000000000n);
    expect(att.vault).toBe(VAULT);
  });

  it("rejects a MintAttestation result", () => {
    expect(() =>
      verifyBurnSignedResult(
        signedFor(BURN, { primaryType: "MintAttestation" }),
        BURN,
        PROJECT,
      ),
    ).toThrow(/not a BurnAttestation/);
  });

  it.each([
    ["operationId", { operationId: `0x${"22".repeat(32)}` }],
    ["amount", { amount: "1" }],
    ["nonce", { nonce: "10" }],
    ["validUntil", { validUntil: 4102444801 }],
    ["metadataDigest", { metadataDigest: `0x${"bb".repeat(32)}` }],
  ])("rejects a digest for a different %s", (_name, change) => {
    const other = { ...BURN, ...change };
    expect(() =>
      verifyBurnSignedResult(signedFor(other), BURN, PROJECT),
    ).toThrow(/different burn/);
  });

  it("rejects a result from another auditor", () => {
    expect(() =>
      verifyBurnSignedResult(
        signedFor(BURN, {
          auditor: "0x1234567890123456789012345678901234567890",
        }),
        BURN,
        PROJECT,
      ),
    ).toThrow(/this project's auditor/);
  });

  it("rejects a burn whose operationId is not keccak256(burnId)", () => {
    const bad = { ...BURN, operationId: `0x${"22".repeat(32)}` };
    expect(() => verifyBurnSignedResult(signedFor(bad), bad, PROJECT)).toThrow(
      /operationId does not match/,
    );
  });
});

describe("parseBurnAmount", () => {
  it("converts whole units with the token decimals", () => {
    expect(parseBurnAmount("0.25", 18, "1000000000000000000")).toBe(
      "250000000000000000",
    );
  });

  it("accepts the remaining amount and rejects one minimal unit more", () => {
    expect(parseBurnAmount("1", 18, "1000000000000000000")).toBe(
      "1000000000000000000",
    );
    expect(() =>
      parseBurnAmount("1.000000000000000001", 18, "1000000000000000000"),
    ).toThrow(/left to burn/);
  });

  it("rejects zero and excess precision", () => {
    expect(() => parseBurnAmount("0", 18, undefined)).toThrow(/above zero/);
    expect(() => parseBurnAmount("1.5", 0, undefined)).toThrow();
  });

  it("skips the remaining check when it is unknown", () => {
    expect(parseBurnAmount("5", 2, undefined)).toBe("500");
  });
});
