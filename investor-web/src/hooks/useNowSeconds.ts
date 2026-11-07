import { useEffect, useState } from "react";

// Cap delays at the signed 32-bit timer limit
const MAX_TIMER_MS = 2 ** 31 - 1;

/**
 * Unix time in seconds, refreshed around the earliest future deadline. Long waits use capped
 * timers and may refresh before the deadline. Empty lists and past or non-finite values schedule
 * nothing. This uses the browser clock; eth_call simulation determines which chain actions are allowed.
 */
export function useNowSeconds(deadlines: readonly number[]): number {
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000));
  let next = 0;
  for (const d of deadlines) {
    if (Number.isFinite(d) && d * 1000 > Date.now() && (next === 0 || d < next))
      next = d;
  }
  useEffect(() => {
    if (next === 0) return;
    const wait = Math.min(next * 1000 - Date.now() + 50, MAX_TIMER_MS);
    const timer = setTimeout(
      () => setNow(Math.floor(Date.now() / 1000)),
      Math.max(wait, 0),
    );
    return () => clearTimeout(timer);
  }, [next, now]);
  return now;
}
