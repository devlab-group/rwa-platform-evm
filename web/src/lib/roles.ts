import { useContext } from "react";
import { keccak256, toBytes, type Address, type Hex } from "viem";
import type { components } from "./api-types";
import { WalletContext } from "../context/walletContextValue";

type RoleHolders = components["schemas"]["RoleHolders"];
type Addresses = components["schemas"]["Addresses"];

/**
 * Projected role names for UI routing; stale/union holder data is advisory, and contracts enforce
 * authorization
 */
export const ROLES = {
  admin: "DEFAULT_ADMIN_ROLE",
  pauser: "PAUSER_ROLE",
  pricer: "PRICER_ROLE",
  treasurer: "TREASURER_ROLE",
  redemptionManager: "REDEMPTION_MANAGER_ROLE",
  compliance: "COMPLIANCE_ROLE",
  incomeManager: "INCOME_MANAGER_ROLE",
} as const;

/** DEFAULT_ADMIN_ROLE is the zero hash, not keccak256("DEFAULT_ADMIN_ROLE") */
export const DEFAULT_ADMIN_ROLE_HASH = `0x${"0".repeat(64)}` as Hex;

/** Role ID is keccak256 of the UTF-8 name, except DEFAULT_ADMIN_ROLE's zero hash */
export function roleHash(role: string): Hex {
  if (role === ROLES.admin) return DEFAULT_ADMIN_ROLE_HASH;
  return keccak256(toBytes(role));
}

/**
 * Role-change targets matching the server's projection model; grant/revoke each listed
 * contract for consistent holders. DEFAULT_ADMIN_ROLE uses two-step transfer instead.
 */
export const ROLE_TARGET_FIELDS: Record<string, (keyof Addresses)[]> = {
  [ROLES.pauser]: ["token"],
  [ROLES.pricer]: ["strategy"],
  [ROLES.treasurer]: ["vault", "redemptionEscrow"],
  [ROLES.redemptionManager]: ["redemptionEscrow"],
  [ROLES.compliance]: ["compliance"],
  [ROLES.incomeManager]: ["incomeDistributor"],
};

/** The roles the admin can grant or revoke in the UI; DEFAULT_ADMIN_ROLE is excluded */
export const GRANTABLE_ROLES = [
  ROLES.pauser,
  ROLES.pricer,
  ROLES.treasurer,
  ROLES.redemptionManager,
  ROLES.compliance,
  ROLES.incomeManager,
] as const;

/** Strategy kind as projected by the server; only a Fixed strategy has roles */
export type StrategyKind = components["schemas"]["Project"]["strategyKind"];

// An unknown kind is treated like Oracle: no role or admin call is sent to an unverified contract
function strategyHasRoles(kind: StrategyKind): boolean {
  return kind === undefined || kind === "Fixed";
}

/** Filters roles by strategy kind and income address; missing kinds retain legacy Fixed behavior */
export function grantableRoles(
  addresses: Addresses | undefined,
  kind: StrategyKind,
): readonly string[] {
  return GRANTABLE_ROLES.filter((r) =>
    r === ROLES.pricer
      ? strategyHasRoles(kind)
      : r === ROLES.incomeManager
        ? Boolean(addresses?.incomeDistributor)
        : true,
  );
}

/** Admin-transfer targets, filtered like grantableRoles and including income when its address exists */
export function adminContractFields(
  addresses: Addresses | undefined,
  kind: StrategyKind,
): (keyof Addresses)[] {
  const fields = ADMIN_CONTRACT_FIELDS.filter(
    (f) => f !== "strategy" || strategyHasRoles(kind),
  );
  return addresses?.incomeDistributor
    ? [...fields, "incomeDistributor"]
    : fields;
}

/** Begin two-step admin transfer on each governance contract */
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
  kind?: StrategyKind,
): Address[] {
  if (role === ROLES.pricer && !strategyHasRoles(kind)) return [];
  const fields = ROLE_TARGET_FIELDS[role] ?? [];
  return resolveAddresses(fields, addresses);
}

/** Resolves the governance contract addresses an admin transfer begins on */
export function adminTransferTargets(
  addresses: Addresses | undefined,
  kind?: StrategyKind,
): Address[] {
  return resolveAddresses(adminContractFields(addresses, kind), addresses);
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

/** Address field names accepted by holdersOnAll */
export type ContractLabel = keyof Addresses;

/**
 * Case-insensitive intersection of role holders across contracts. Without either map or list,
 * returns the union; an empty list returns none.
 */
export function holdersOnAll(
  roles: RoleHolders | undefined,
  rolesByContract: Record<string, RoleHolders> | undefined,
  role: string,
  contracts: readonly ContractLabel[] | undefined,
): string[] {
  if (!rolesByContract || !contracts) return roles?.[role] ?? [];
  const lists = contracts.map((c) => rolesByContract[c]?.[role] ?? []);
  if (lists.length === 0) return [];
  return lists[0].filter((h) =>
    lists.every((l) => l.some((x) => x.toLowerCase() === h.toLowerCase())),
  );
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

/** Checks wallet membership in the holdersOnAll result */
export function useHasRole(
  roles: RoleHolders | undefined,
  role: string,
  rolesByContract?: Record<string, RoleHolders>,
  contracts?: readonly ContractLabel[],
): boolean {
  const ctx = useContext(WalletContext);
  return hasRole(
    { [role]: holdersOnAll(roles, rolesByContract, role, contracts) },
    role,
    ctx?.address ?? null,
  );
}
