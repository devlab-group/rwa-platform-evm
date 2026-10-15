import { useId, useState, type ReactNode } from "react";
import type { Address } from "viem";
import type { components } from "../lib/api-types";
import { useHasRole } from "../lib/roles";
import { TxRouteContext, type TxRoute } from "../lib/txRoute";

type RoleHolders = components["schemas"]["RoleHolders"];

/**
 * Routes role-holding wallets directly; other callers select a holder for Safe export.
 * Nested forms use useTxRoute, function children receive it. Selection checks no Safe
 * bytecode; contracts enforce authorization.
 */
export function RoleGate({
  roles,
  role,
  action,
  children,
}: {
  roles: RoleHolders | undefined;
  /** On-chain role name, such as ROLES.treasurer */
  role: string;
  /** Short verb phrase describing the action in the fallback notice */
  action: string;
  children: ReactNode | ((route: TxRoute) => ReactNode);
}) {
  const allowed = useHasRole(roles, role);
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
  const holders = roles?.[role] ?? [];
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
