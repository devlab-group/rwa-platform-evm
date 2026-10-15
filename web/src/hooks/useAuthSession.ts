import { useSyncExternalStore } from "react";
import {
  getSession,
  subscribeSession,
  type AuthSession,
} from "../lib/authSession";

/** Live view of the admin session; undefined when signed out */
export function useAuthSession(): AuthSession | undefined {
  return useSyncExternalStore(subscribeSession, getSession, getSession);
}
