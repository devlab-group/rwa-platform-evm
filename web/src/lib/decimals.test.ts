import { beforeEach, describe, expect, it, vi } from "vitest";
import { resolveQuoteDecimals } from "./decimals";
import { readErc20Decimals } from "./wallet";
import type { components } from "./api-types";

type Project = components["schemas"]["Project"];

// Post-deploy screens already fetch the project; when the server supplies
// Project.quoteDecimals they must scale quote amounts from it instead of an
// on-chain round trip, falling back to the ERC-20 read only when it's absent
// (older server builds). readErc20Decimals is the on-chain path, so a strict
// "not called" assertion proves the server value was preferred.
vi.mock("./wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./wallet")>();
  return { ...actual, readErc20Decimals: vi.fn().mockResolvedValue(18) };
});

const QUOTE = "0x0000000000000000000000000000000000000002";

describe("resolveQuoteDecimals", () => {
  beforeEach(() => {
    vi.mocked(readErc20Decimals).mockClear().mockResolvedValue(18);
  });

  it("prefers Project.quoteDecimals and does not read on-chain", async () => {
    const project: Project = {
      chainId: 31337,
      quoteDecimals: 6,
      addresses: { quoteToken: QUOTE },
    };

    await expect(resolveQuoteDecimals(project, 31337)).resolves.toBe(6);
    expect(readErc20Decimals).not.toHaveBeenCalled();
  });

  it("falls back to the on-chain read when quoteDecimals is absent", async () => {
    const project: Project = {
      chainId: 31337,
      addresses: { quoteToken: QUOTE },
    };

    await expect(resolveQuoteDecimals(project, 31337)).resolves.toBe(18);
    expect(readErc20Decimals).toHaveBeenCalledWith(31337, QUOTE);
  });

  it("uses quoteDecimals even when it is 0 (a real, valid value)", async () => {
    const project: Project = {
      chainId: 31337,
      quoteDecimals: 0,
      addresses: { quoteToken: QUOTE },
    };

    await expect(resolveQuoteDecimals(project, 31337)).resolves.toBe(0);
    expect(readErc20Decimals).not.toHaveBeenCalled();
  });
});
