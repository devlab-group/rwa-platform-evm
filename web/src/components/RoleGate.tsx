import { useId, useState, type ReactNode } from "react";
import type { Address } from "viem";
import type { components } from "../lib/api-types";
import { holdersOnAll, useHasRole, type ContractLabel } from "../lib/roles";
import { TxRouteContext, type TxRoute } from "../lib/txRoute";

type RoleHolders = components["schemas"]["RoleHolders"];

/**
 * Routes callers through holdersOnAll: qualified wallets submit directly; others select a holder
 * for Safe export. Selection does not verify Safe bytecode; contracts enforce authorization. Nested
 * forms use useTxRoute; function children receive the route.
 */
export function RoleGate({
  roles,
  rolesByContract,
  contracts,
  role,
  action,
  children,
}: {
  roles: RoleHolders | undefined;
  /** Holders per contract label; absent on older servers or before projection */
  rolesByContract?: Record<string, RoleHolders>;
  /** Contracts the gated calls target; the role is required on each */
  contracts?: readonly ContractLabel[];
  /** On-chain role name, such as ROLES.treasurer */
  role: string;
  /** Short verb phrase describing the action in the fallback notice */
  action: string;
  children: ReactNode | ((route: TxRoute) => ReactNode);
}) {
  const allowed = useHasRole(roles, role, rolesByContract, contracts);
  const [safe, setSafe] = useState("");
  const selectId = useId();

  function render(route: TxRoute) {
    return (
      <TxRouteContext.Provider value={route}>
        {typeof children === "function" ? children(route) : children}
      </TxRouteContext.Provider>
    );
  }

  if (allowed) return render({ kind: "wallet" });
  const holders = holdersOnAll(roles, rolesByContract, role, contracts);
  return (
    <>
      <p className="field__hint" role="status">
        Connect a wallet holding <code className="mono">{role}</code> to{" "}
        {action}.
      </p>
      {holders.length > 0 && (
        <div className="field">
          <label htmlFor={selectId}>
            Save a Safe{"{Wallet}"} batch using a Safe holding {role}
          </label>
          <select
            id={selectId}
            value={safe}
            onChange={(e) => setSafe(e.target.value)}
          >
            <option value="">Choose a Safe…</option>
            {holders.map((holder) => (
              <option key={holder} value={holder}>
                {holder}
              </option>
            ))}
          </select>
        </div>
      )}
      {safe && render({ kind: "safe", safe: safe as Address })}
    </>
  );
}
