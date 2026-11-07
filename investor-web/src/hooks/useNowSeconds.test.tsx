import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useNowSeconds } from "./useNowSeconds";

const START = new Date("2030-01-01T00:00:00Z");
const secs = () => Math.floor(Date.now() / 1000);

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(START);
});
afterEach(() => {
  vi.useRealTimers();
});

describe("useNowSeconds", () => {
  it("starts at the current time and schedules nothing without a future deadline", () => {
    const { result } = renderHook(() => useNowSeconds([secs() - 10, NaN]));
    expect(result.current).toBe(secs());
    expect(vi.getTimerCount()).toBe(0);
  });

  it("moves past the earliest future deadline with one timer, then waits for the next", () => {
    const t0 = secs();
    const { result } = renderHook(() => useNowSeconds([t0 + 20, t0 + 5]));
    expect(vi.getTimerCount()).toBe(1);

    // Nothing re-renders before the deadline
    act(() => {
      vi.advanceTimersByTime(4_000);
    });
    expect(result.current).toBe(t0);

    act(() => {
      vi.advanceTimersByTime(2_000);
    });
    expect(result.current).toBeGreaterThanOrEqual(t0 + 5);
    // The 5 s deadline is behind us; the 20 s one now owns the single timer
    expect(vi.getTimerCount()).toBe(1);

    act(() => {
      vi.advanceTimersByTime(15_000);
    });
    expect(result.current).toBeGreaterThanOrEqual(t0 + 20);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("clears its timer on unmount", () => {
    const { unmount } = renderHook(() => useNowSeconds([secs() + 60]));
    expect(vi.getTimerCount()).toBe(1);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
