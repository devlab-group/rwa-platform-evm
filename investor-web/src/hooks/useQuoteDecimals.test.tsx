import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useQuoteDecimals } from "./useQuoteDecimals";
import { resolveQuoteDecimals } from "../lib/decimals";

vi.mock("../lib/decimals", () => ({ resolveQuoteDecimals: vi.fn() }));

const QUOTE = "0x0000000000000000000000000000000000000002";

beforeEach(() => {
  vi.mocked(resolveQuoteDecimals).mockReset();
});

describe("useQuoteDecimals", () => {
  it("returns decimals the project carries on the first render, before any read finishes", () => {
    vi.mocked(resolveQuoteDecimals).mockReturnValue(new Promise(() => {}));
    const { result } = renderHook(() =>
      useQuoteDecimals(
        { quoteDecimals: 6, addresses: { quoteToken: QUOTE } },
        31337,
      ),
    );
    expect(result.current).toBe(6);
  });

  it("keeps zero decimals as a known value", () => {
    vi.mocked(resolveQuoteDecimals).mockReturnValue(new Promise(() => {}));
    const { result } = renderHook(() =>
      useQuoteDecimals({ quoteDecimals: 0 }, 31337),
    );
    expect(result.current).toBe(0);
  });

  it("is undefined while the on-chain read runs, then the value", async () => {
    vi.mocked(resolveQuoteDecimals).mockResolvedValue(18);
    const { result } = renderHook(() =>
      useQuoteDecimals({ addresses: { quoteToken: QUOTE } }, 31337),
    );
    expect(result.current).toBeUndefined();
    await waitFor(() => expect(result.current).toBe(18));
  });

  it("stays undefined when the read fails", async () => {
    vi.mocked(resolveQuoteDecimals).mockRejectedValue(new Error("no rpc"));
    const { result } = renderHook(() =>
      useQuoteDecimals({ addresses: { quoteToken: QUOTE } }, 31337),
    );
    await waitFor(() => expect(resolveQuoteDecimals).toHaveBeenCalled());
    expect(result.current).toBeUndefined();
  });
});
