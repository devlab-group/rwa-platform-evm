import { describe, expect, it } from "vitest";
import { sha256, toBytes } from "viem";
import { encodeReasonCode } from "./redemption";

describe("encodeReasonCode", () => {
  it("passes a 0x + 64-hex bytes32 through unchanged", () => {
    const bytes32 = `0x${"ab".repeat(32)}`;
    expect(encodeReasonCode(bytes32)).toBe(bytes32);
  });

  it("hashes free text with sha256 of its UTF-8 bytes", () => {
    const text = "NON_COMPLIANT";
    expect(encodeReasonCode(text)).toBe(sha256(toBytes(text)));
  });

  it("hashes a 0x string that is not exactly 64 hex chars (treated as text)", () => {
    const notBytes32 = "0xdeadbeef";
    expect(encodeReasonCode(notBytes32)).toBe(sha256(toBytes(notBytes32)));
  });

  it("rejects an empty reason code", () => {
    expect(() => encodeReasonCode("")).toThrow();
  });
});
