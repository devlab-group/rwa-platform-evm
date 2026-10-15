// Signer --policy trust root: six required keys below; rejects unknown keys.
// Optional maxAttestationLifetimeHours and safe are supported by the signer but not written here.
import type { components } from "./api-types";

type Project = components["schemas"]["Project"];

export interface SignerPolicy {
  /** Decimal integer string, e.g. "31337"; not a JSON number */
  chainId: string;
  /** SupplyController address */
  controller: string;
  /** Vault address */
  vault: string;
  /** Project auditor address */
  auditor: string;
  /** The project UUID string, not the bytes32 profile digest */
  projectId: string;
  /** 0x-prefixed bytes32 profile digest */
  profileDigest: string;
}

/** Builds policy from project/profile UUID; returns null if any required value is absent */
export function buildSignerPolicy(
  project: Project | undefined,
  projectUuid: string | undefined,
): SignerPolicy | null {
  if (!project) return null;
  const controller = project.addresses?.supplyController;
  const vault = project.addresses?.vault;
  const { chainId, auditor, profileDigest } = project;
  if (
    chainId === undefined ||
    !controller ||
    !vault ||
    !auditor ||
    !profileDigest ||
    !projectUuid
  ) {
    return null;
  }
  return {
    chainId: String(chainId),
    controller,
    vault,
    auditor,
    projectId: projectUuid,
    profileDigest,
  };
}
