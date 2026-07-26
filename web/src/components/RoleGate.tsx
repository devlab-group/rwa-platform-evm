import type { ReactNode } from "react";
import type { components } from "../lib/api-types";
import { useHasRole } from "../lib/roles";

type RoleHolders = components["schemas"]["RoleHolders"];

/**
 * Renders `children` only when the connected wallet holds `role`; otherwise a
 * muted notice naming the role required to perform `action`. Gates admin
 * actions in the UI — the underlying on-chain authorization is still enforced
 * by the contract when the built calldata is submitted (see lib/roles.ts).
 */
export function RoleGate({
  roles,
  role,
  action,
  children,
}: {
  roles: RoleHolders | undefined;
  /** On-chain role name, e.g. ROLES.treasurer ("TREASURER_ROLE"). */
  role: string;
  /** Short verb phrase used in the fallback notice, e.g. "build treasury withdrawal calldata". */
  action: string;
  children: ReactNode;
}) {
  const allowed = useHasRole(roles, role);
  if (allowed) return <>{children}</>;
  return (
    <p className="field__hint" role="status">
      Connect a wallet holding <code className="mono">{role}</code> to {action}.
    </p>
  );
}
