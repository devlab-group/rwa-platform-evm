import {
  encodeAbiParameters,
  keccak256,
  parseAbiParameters,
  toBytes,
  toHex,
} from "viem";
import type { components } from "../lib/api-types";

type Burn = components["schemas"]["Burn"];
type Project = components["schemas"]["Project"];
type SignedResult = components["schemas"]["SignedResult"];

export const AUDITOR = "0x9999999999999999999999999999999999999999";
export const CONTROLLER = "0x0000000000000000000000000000000000000044";
export const VAULT = "0x0000000000000000000000000000000000000055";
export const BURN_ID = "11111111-2222-4333-8444-555555555555";

export const PROJECT: Project = {
  chainId: 31337,
  decimals: 18,
  auditor: AUDITOR,
  profileDigest: `0x${"cc".repeat(32)}`,
  addresses: { supplyController: CONTROLLER, vault: VAULT },
};

export const BURN: Burn = {
  burnId: BURN_ID,
  recordId: "record-001",
  operationId: keccak256(toBytes(BURN_ID)),
  amount: "250000000000000000",
  status: "Pending",
  metadataDigest: `0x${"aa".repeat(32)}`,
  nonce: "9",
  validUntil: 4102444800,
};

/** EIP-712 digest assembled from shared/eip712/types.md without viem's typed-data hasher */
export function expectedDigest(b: Burn): `0x${string}` {
  const typehash = keccak256(
    toHex(
      "BurnAttestation(address auditor,bytes32 profileDigest,bytes32 operationId,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)",
    ),
  );
  const domainTypehash = keccak256(
    toHex(
      "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)",
    ),
  );
  const domainSeparator = keccak256(
    encodeAbiParameters(
      parseAbiParameters("bytes32,bytes32,bytes32,uint256,address"),
      [
        domainTypehash,
        keccak256(toHex("RWA-Supply-Attestation")),
        keccak256(toHex("1")),
        31337n,
        CONTROLLER,
      ],
    ),
  );
  const structHash = keccak256(
    encodeAbiParameters(
      parseAbiParameters(
        "bytes32,address,bytes32,bytes32,bytes32,uint256,uint256,uint64,address",
      ),
      [
        typehash,
        AUDITOR,
        PROJECT.profileDigest as `0x${string}`,
        b.operationId as `0x${string}`,
        b.metadataDigest as `0x${string}`,
        BigInt(b.amount!),
        BigInt(b.nonce!),
        BigInt(b.validUntil!),
        VAULT,
      ],
    ),
  );
  return keccak256(`0x1901${domainSeparator.slice(2)}${structHash.slice(2)}`);
}

export function signedFor(
  b: Burn,
  over: Partial<SignedResult> = {},
): SignedResult {
  return {
    formatVersion: "1.0",
    auditor: AUDITOR,
    primaryType: "BurnAttestation",
    typedDataDigest: expectedDigest(b),
    signature: `0x${"ab".repeat(65)}`,
    signedAt: "2026-01-01T00:00:00Z",
    ...over,
  };
}
