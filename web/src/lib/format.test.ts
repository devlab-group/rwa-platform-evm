import { describe, expect, it } from "vitest";
import {
  AmountFormatError,
  formatTokenAmount,
  shortenAddress,
  toMinimalUnits,
} from "./format";

describe("formatTokenAmount", () => {
  it("groups a raw integer string when no decimals are given", () => {
    expect(formatTokenAmount("1234567")).toBe("1,234,567");
  });

  it("scales to whole units when decimals are given", () => {
    expect(formatTokenAmount("1500000000000000000", 18)).toBe("1.5");
  });

  it("passes through non-numeric input unchanged", () => {
    expect(formatTokenAmount("not-a-number")).toBe("not-a-number");
  });

  it("returns an em dash for empty input", () => {
    expect(formatTokenAmount("")).toBe("—");
  });
});

describe("toMinimalUnits", () => {
  it("scales a whole-unit integer to 18-decimal minimal units", () => {
    expect(toMinimalUnits("1", 18)).toBe("1000000000000000000");
  });

  it("scales a whole-unit integer to 6-decimal minimal units", () => {
    // The req_changes Web 1 worked example: user enters 1 for a 6-decimal
    // token -> 1000000 is sent to the server.
    expect(toMinimalUnits("1", 6)).toBe("1000000");
  });

  it("scales a fractional value at the token's full precision (6 decimals)", () => {
    expect(toMinimalUnits("1.234567", 6)).toBe("1234567");
  });

  it("scales a fractional value at full precision (18 decimals)", () => {
    expect(toMinimalUnits("1.5", 18)).toBe("1500000000000000000");
  });

  it("accepts a leading-dot fractional value", () => {
    expect(toMinimalUnits(".5", 6)).toBe("500000");
  });

  it("trims surrounding whitespace", () => {
    expect(toMinimalUnits("  2  ", 6)).toBe("2000000");
  });

  it("maps zero to zero", () => {
    expect(toMinimalUnits("0", 18)).toBe("0");
    expect(toMinimalUnits("0.0", 18)).toBe("0");
  });

  it("round-trips human -> minimal -> human for 6 decimals", () => {
    const minimal = toMinimalUnits("1234.56", 6);
    expect(minimal).toBe("1234560000");
    expect(formatTokenAmount(minimal, 6)).toBe("1,234.56");
  });

  it("round-trips human -> minimal -> human for 18 decimals", () => {
    const minimal = toMinimalUnits("0.000000000000000001", 18);
    expect(minimal).toBe("1");
    expect(formatTokenAmount(minimal, 18)).toBe("0.000000000000000001");
  });

  it("rejects over-precise input rather than silently rounding (6 decimals)", () => {
    // viem's parseUnits alone would round "1.2345678" -> 1234568; the guard
    // must reject it so money is never silently mis-scaled.
    expect(() => toMinimalUnits("1.2345678", 6)).toThrow(AmountFormatError);
  });

  it("rejects any fractional part for a 0-decimal token", () => {
    expect(() => toMinimalUnits("1.5", 0)).toThrow(AmountFormatError);
  });

  it("rejects an empty string (parseUnits would coerce it to 0)", () => {
    expect(() => toMinimalUnits("", 18)).toThrow(AmountFormatError);
  });

  it("rejects a negative amount", () => {
    expect(() => toMinimalUnits("-1", 18)).toThrow(AmountFormatError);
  });

  it("rejects non-numeric, exponent, and grouped input", () => {
    expect(() => toMinimalUnits("abc", 18)).toThrow(AmountFormatError);
    expect(() => toMinimalUnits("1e3", 18)).toThrow(AmountFormatError);
    expect(() => toMinimalUnits("1,000", 18)).toThrow(AmountFormatError);
    expect(() => toMinimalUnits("1.5.5", 18)).toThrow(AmountFormatError);
  });

  it("echoes the entered value in the error message", () => {
    expect(() => toMinimalUnits("12.oops", 6)).toThrow(/"12\.oops"/);
  });
});

describe("shortenAddress", () => {
  it("shortens a full address", () => {
    expect(shortenAddress("0x1234567890abcdef1234567890abcdef12345678")).toBe(
      "0x1234…5678",
    );
  });

  it("returns an em dash for a missing address", () => {
    expect(shortenAddress(undefined)).toBe("—");
  });
});
