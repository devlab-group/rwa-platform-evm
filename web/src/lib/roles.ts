import { useContext } from "react";
import { keccak256, toBytes, type Address, type Hex } from "viem";
import type { components } from "./api-types";
import { WalletContext } from "../context/walletContextValue";

type RoleHolders = components["schemas"]["RoleHolders"];
type Addresses = components["schemas"]["Addresses"];

/**
 * On-chain AccessControl role names, exactly as the server projects them into
 * Project.roles (server/internal/project/security.go). Used to gate admin
 * actions in the UI by the connected wallet's role.
 *
 * Note this gate is a UX convenience, not a security boundary: the calldata the
 * gated forms build is still authorized on-chain by the contract's `onlyRole`
 * modifier when it is finally submitted. Hiding an action the connected wallet
 * can't perform just avoids building calldata that would revert.
 */
export const ROLES = {
  admin: "DEFAULT_ADMIN_ROLE",
  pauser: "PAUSER_ROLE",
  pricer: "PRICER_ROLE",
  treasurer: "TREASURER_ROLE",
  redemptionManager: "REDEMPTION_MANAGER_ROLE",
  compliance: "COMPLIANCE_ROLE",
} as const;

/** The DEFAULT_ADMIN_ROLE bytes32 is the zero hash, not keccak256("DEFAULT_ADMIN_ROLE"). */
export const DEFAULT_ADMIN_ROLE_HASH = `0x${"0".repeat(64)}` as Hex;

/**
 * The on-chain `bytes32` role id: OpenZeppelin AccessControl computes it as
 * keccak256 of the role name's UTF-8 bytes (DEFAULT_ADMIN_ROLE being the zero
 * hash special case). Used to encode grantRole/revokeRole calls.
 */
export function roleHash(role: string): Hex {
  if (role === ROLES.admin) return DEFAULT_ADMIN_ROLE_HASH;
  return keccak256(toBytes(role));
}

/**
 * Which deployed contracts each granular role is held on, exactly mirroring how
 * the server provisions and projects them (server/internal/project/security.go).
 * Granting or revoking a role must target every contract in its set to keep the
 * on-chain holders consistent with the platform's model. DEFAULT_ADMIN_ROLE is
 * excluded here — it is changed via the two-step admin transfer, not grantRole.
 */
export const ROLE_TARGET_FIELDS: Record<string, (keyof Addresses)[]> = {
  [ROLES.pauser]: ["token"],
  [ROLES.pricer]: ["vault", "strategy"],
  [ROLES.treasurer]: ["vault", "redemptionEscrow"],
  [ROLES.redemptionManager]: ["redemptionEscrow"],
  [ROLES.compliance]: ["compliance"],
};

/** The roles a DEFAULT_ADMIN can grant/revoke from the UI (admin itself excluded — see transfer flow). */
export const GRANTABLE_ROLES = [
  ROLES.pauser,
  ROLES.pricer,
  ROLES.treasurer,
  ROLES.redemptionManager,
  ROLES.compliance,
] as const;

/** Every governance contract carries DEFAULT_ADMIN_ROLE, so an admin transfer must be begun on each. */
export const ADMIN_CONTRACT_FIELDS: (keyof Addresses)[] = [
  "token",
  "compliance",
  "supplyController",
  "vault",
  "redemptionEscrow",
  "strategy",
];

/** Resolves the deployed contract addresses a role change must be sent to. */
export function roleTargets(
  role: string,
  addresses: Addresses | undefined,
): Address[] {
  const fields = ROLE_TARGET_FIELDS[role] ?? [];
  return resolveAddresses(fields, addresses);
}

/** Resolves the governance contract addresses an admin transfer must be begun on. */
export function adminTransferTargets(
  addresses: Addresses | undefined,
): Address[] {
  return resolveAddresses(ADMIN_CONTRACT_FIELDS, addresses);
}

function resolveAddresses(
  fields: (keyof Addresses)[],
  addresses: Addresses | undefined,
): Address[] {
  if (!addresses) return [];
  const out: Address[] = [];
  for (const f of fields) {
    const a = addresses[f];
    if (typeof a === "string" && a) out.push(a as Address);
  }
  return out;
}

/** Whether `address` is one of the holders of `role` in the project's role map. */
export function hasRole(
  roles: RoleHolders | undefined,
  role: string,
  address: string | null | undefined,
): boolean {
  if (!roles || !address) return false;
  const holders = roles[role];
  if (!holders) return false;
  const target = address.toLowerCase();
  return holders.some((h) => h.toLowerCase() === target);
}

/**
 * Whether the connected wallet holds `role` in the given project role map.
 * `false` while the wallet is disconnected, the roles haven't loaded, or the
 * hook is used outside a <WalletProvider> (read defensively so a gated form
 * simply reports "no access" rather than throwing).
 */
export function useHasRole(
  roles: RoleHolders | undefined,
  role: string,
): boolean {
  const ctx = useContext(WalletContext);
  return hasRole(roles, role, ctx?.address ?? null);
}
