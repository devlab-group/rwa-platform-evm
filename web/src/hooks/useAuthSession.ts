import { useSyncExternalStore } from "react";
import {
  getSession,
  subscribeSession,
  type AuthSession,
} from "../lib/authSession";

/** Live view of the in-memory admin session. Undefined = signed out. */
export function useAuthSession(): AuthSession | undefined {
  return useSyncExternalStore(subscribeSession, getSession, getSession);
}
