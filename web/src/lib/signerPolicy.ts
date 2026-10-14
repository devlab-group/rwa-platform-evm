// The offline signer's trust-root policy: the `--policy policy.json` file the
// admin hands the auditor. The signer requires these six keys and rejects
// unknown ones. It also accepts optional `maxAttestationLifetimeHours` and
// `safe` keys, which the console does not write.
import type { components } from "./api-types";

type Project = components["schemas"]["Project"];

export interface SignerPolicy {
  /** Decimal string such as "31337", not a JSON number; the signer parses it as a decimal integer string */
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

/**
 * Assembles the signer policy from the loaded project and the profile UUID.
 * Returns null when any of the six values is missing, for example before the
 * project is deployed, so the download is never a partial file.
 */
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
