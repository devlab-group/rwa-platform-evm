import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { verifyBurnSignedResult } from "./burn";

// verifyBurnSignedResult must reproduce the shared Solidity/Go burn vector's type layout and domain.
// The console does not recompute mint digests; signer and contract tests check that vector.
function load(name: string) {
  return JSON.parse(readFileSync(`../shared/vectors/${name}`, "utf8"));
}

describe("burn-eip712.json against the console's digest code", () => {
  const v = load("burn-eip712.json");
  const project = {
    chainId: v.domain.chainId,
    auditor: v.message.auditor,
    profileDigest: v.message.profileDigest,
    addresses: {
      supplyController: v.domain.verifyingContract,
      vault: v.message.vault,
    },
  };
  const burn = {
    burnId: v.message.burnId,
    operationId: v.message.operationId,
    metadataDigest: v.message.metadataDigest,
    amount: v.message.amount,
    nonce: v.message.nonce,
    validUntil: v.message.validUntil,
  };
  const signed = {
    formatVersion: "1.0",
    auditor: v.message.auditor,
    primaryType: "BurnAttestation",
    typedDataDigest: v.digest,
    signature: v.signature,
    signedAt: "2026-01-01T00:00:00Z",
  };

  it("reproduces the vector digest and builds the attested values", () => {
    expect(verifyBurnSignedResult(signed, burn, project)).toEqual({
      auditor: v.message.auditor,
      profileDigest: v.message.profileDigest,
      operationId: v.message.operationId,
      metadataDigest: v.message.metadataDigest,
      amount: 250000000000000000000n,
      nonce: 43n,
      validUntil: 1800000000n,
      vault: v.message.vault,
    });
  });

  it("rejects the vector digest when the amount differs by one unit", () => {
    expect(() =>
      verifyBurnSignedResult(
        signed,
        { ...burn, amount: "250000000000000000001" },
        project,
      ),
    ).toThrow(/different burn digest/);
  });
});
