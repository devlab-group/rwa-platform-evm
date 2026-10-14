import { createContext, useContext } from "react";
import type { Address } from "viem";

/** Selects direct wallet submission or a batch exported for Safe owner approval */
export type TxRoute = { kind: "wallet" } | { kind: "safe"; safe: Address };

export const TxRouteContext = createContext<TxRoute>({ kind: "wallet" });

/** Returns the enclosing RoleGate route, defaulting to wallet submission */
export function useTxRoute(): TxRoute {
  return useContext(TxRouteContext);
}
