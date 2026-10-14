import { useContext } from "react";
import { keccak256, toBytes, type Address, type Hex } from "viem";
import type { components } from "./api-types";
import { WalletContext } from "../context/walletContextValue";

type RoleHolders = components["schemas"]["RoleHolders"];
type Addresses = components["schemas"]["Addresses"];

/**
 * On-chain AccessControl role names, as the server projects them into
 * Project.roles. The UI uses them to gate admin actions by the connected
 * wallet's role.
 *
 * The gate is a convenience, not a security boundary: the contract's `onlyRole`
 * check authorizes every call. Hiding an action the wallet cannot perform only
 * avoids submitting a call that would revert.
 */
export const ROLES = {
  admin: "DEFAULT_ADMIN_ROLE",
  pauser: "PAUSER_ROLE",
  pricer: "PRICER_ROLE",
  treasurer: "TREASURER_ROLE",
  redemptionManager: "REDEMPTION_MANAGER_ROLE",
  compliance: "COMPLIANCE_ROLE",
} as const;

/** DEFAULT_ADMIN_ROLE is the zero hash, not keccak256("DEFAULT_ADMIN_ROLE") */
export const DEFAULT_ADMIN_ROLE_HASH = `0x${"0".repeat(64)}` as Hex;

/**
 * The on-chain bytes32 role id for grantRole and revokeRole calls: the keccak256
 * of the role name's UTF-8 bytes, except DEFAULT_ADMIN_ROLE, which is the zero hash.
 */
export function roleHash(role: string): Hex {
  if (role === ROLES.admin) return DEFAULT_ADMIN_ROLE_HASH;
  return keccak256(toBytes(role));
}

/**
 * The deployed contracts that hold each role, matching how the server projects
 * holders (server/internal/project/security.go). Grant or revoke a role on
 * every contract in its list so the holders stay consistent. DEFAULT_ADMIN_ROLE
 * is excluded: it changes through the two-step admin transfer, not grantRole.
 */
export const ROLE_TARGET_FIELDS: Record<string, (keyof Addresses)[]> = {
  [ROLES.pauser]: ["token"],
  [ROLES.pricer]: ["strategy"],
  [ROLES.treasurer]: ["vault", "redemptionEscrow"],
  [ROLES.redemptionManager]: ["redemptionEscrow"],
  [ROLES.compliance]: ["compliance"],
};

/** The roles the admin can grant or revoke in the UI; DEFAULT_ADMIN_ROLE is excluded */
export const GRANTABLE_ROLES = [
  ROLES.pauser,
  ROLES.pricer,
  ROLES.treasurer,
  ROLES.redemptionManager,
  ROLES.compliance,
] as const;

/** Every governance contract holds DEFAULT_ADMIN_ROLE, so an admin transfer begins on each */
export const ADMIN_CONTRACT_FIELDS: (keyof Addresses)[] = [
  "token",
  "compliance",
  "supplyController",
  "vault",
  "redemptionEscrow",
  "strategy",
];

/** Resolves the deployed contract addresses a role change targets */
export function roleTargets(
  role: string,
  addresses: Addresses | undefined,
): Address[] {
  const fields = ROLE_TARGET_FIELDS[role] ?? [];
  return resolveAddresses(fields, addresses);
}

/** Resolves the governance contract addresses an admin transfer begins on */
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
 * Whether the connected wallet holds `role` in the project role map. Returns
 * false while the wallet is disconnected, the roles are unloaded, or the hook
 * runs outside a WalletProvider.
 */
export function useHasRole(
  roles: RoleHolders | undefined,
  role: string,
): boolean {
  const ctx = useContext(WalletContext);
  return hasRole(roles, role, ctx?.address ?? null);
}
