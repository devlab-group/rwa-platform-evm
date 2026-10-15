import {
  hashTypedData,
  keccak256,
  toBytes,
  type Address,
  type Hex,
} from "viem";
import type { components } from "./api-types";
import type { BurnAttestationInput } from "./wallet";
import { toMinimalUnits } from "./format";

type Burn = components["schemas"]["Burn"];
type SignedResult = components["schemas"]["SignedResult"];
type Project = components["schemas"]["Project"];

const BURN_TYPES = {
  BurnAttestation: [
    { name: "auditor", type: "address" },
    { name: "profileDigest", type: "bytes32" },
    { name: "operationId", type: "bytes32" },
    { name: "metadataDigest", type: "bytes32" },
    { name: "amount", type: "uint256" },
    { name: "nonce", type: "uint256" },
    { name: "validUntil", type: "uint64" },
    { name: "vault", type: "address" },
  ],
} as const;

/** Converts whole units to positive minimal units, enforcing remaining when known */
export function parseBurnAmount(
  human: string,
  decimals: number,
  remaining: string | undefined,
): string {
  const minimal = toMinimalUnits(human, decimals);
  if (BigInt(minimal) === 0n) throw new Error("The amount must be above zero.");
  if (remaining !== undefined && BigInt(minimal) > BigInt(remaining)) {
    throw new Error("Amount exceeds what the record has left to burn.");
  }
  return minimal;
}

/** Builds the auditor's attestation from burn and project fields */
export function buildBurnAttestation(
  burn: Burn,
  project: Project,
): BurnAttestationInput {
  const vault = project.addresses?.vault;
  if (
    !project.auditor ||
    !project.profileDigest ||
    !vault ||
    burn.operationId === undefined ||
    burn.metadataDigest === undefined ||
    burn.amount === undefined ||
    burn.nonce === undefined ||
    burn.validUntil === undefined
  ) {
    throw new Error(
      "Missing burn attestation data. Check auditor, profileDigest, Vault, operationId, metadataDigest, amount, nonce and validUntil.",
    );
  }
  return {
    auditor: project.auditor as Address,
    profileDigest: project.profileDigest as Hex,
    operationId: burn.operationId as Hex,
    metadataDigest: burn.metadataDigest as Hex,
    amount: BigInt(burn.amount),
    nonce: BigInt(burn.nonce),
    validUntil: BigInt(burn.validUntil),
    vault: vault as Address,
  };
}

/**
 * Matches the result's type, declared auditor and digest to this burn;
 * SupplyController verifies the signature
 */
export function verifyBurnSignedResult(
  signed: SignedResult,
  burn: Burn,
  project: Project,
): BurnAttestationInput {
  if (signed.primaryType !== "BurnAttestation") {
    throw new Error(
      `This is a ${signed.primaryType} result, not a BurnAttestation. Upload the burn package's result.`,
    );
  }
  const supplyController = project.addresses?.supplyController;
  if (!supplyController || project.chainId === undefined) {
    throw new Error(
      "Deployment is incomplete: SupplyController or chain is missing.",
    );
  }
  const attestation = buildBurnAttestation(burn, project);
  if (burn.operationId !== keccak256(toBytes(burn.burnId ?? ""))) {
    throw new Error("The burn's operationId does not match its burnId.");
  }
  if (signed.auditor.toLowerCase() !== attestation.auditor.toLowerCase()) {
    throw new Error(
      `The result names ${signed.auditor}; this project's auditor is ${attestation.auditor}. Upload the correct signed-result.json.`,
    );
  }
  const digest = hashTypedData({
    domain: {
      name: "RWA-Supply-Attestation",
      version: "1",
      chainId: project.chainId,
      verifyingContract: supplyController as Address,
    },
    types: BURN_TYPES,
    primaryType: "BurnAttestation",
    message: attestation,
  });
  if (digest.toLowerCase() !== signed.typedDataDigest.toLowerCase()) {
    throw new Error(
      "This result has a different burn digest. Use this burn's latest package; reissue changes its nonce. Check operationId, amount, nonce, validUntil and metadataDigest.",
    );
  }
  return attestation;
}
