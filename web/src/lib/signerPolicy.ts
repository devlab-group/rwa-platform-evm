// The offline signer's trust-root policy (the `--policy policy.json` file the
// admin hands the auditor). The signer parses it with DisallowUnknownFields, so
// it MUST carry exactly these six keys and nothing else — no lifetime override
// (the signer uses its default when absent). See signer/CLAUDE.md.
import type { components } from "./api-types";

type Project = components["schemas"]["Project"];

export interface SignerPolicy {
  /** Decimal STRING (e.g. "31337"), never a JSON number — the loader validates it as a decimal string. */
  chainId: string;
  /** SupplyController address. */
  controller: string;
  /** Vault address. */
  vault: string;
  /** Project auditor address. */
  auditor: string;
  /** The project UUID string (NOT the bytes32 profileDigest/keccak). */
  projectId: string;
  /** 0x-prefixed bytes32 profile digest. */
  profileDigest: string;
}

/**
 * Assembles the signer policy from the loaded project + the profile's UUID, or
 * returns null when the project isn't deployed yet / the profile is missing (any
 * of the six values absent) — so the download is never a broken/partial file.
 * The deployed contract addresses only exist post-deploy, so their presence is
 * the deployed gate. Key order matches the signer's documented shape.
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
