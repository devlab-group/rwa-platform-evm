import { describe, expect, it } from "vitest";
import { parseSignedResult } from "./signedResult";

const VALID = {
  formatVersion: "1.0",
  auditor: "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
  primaryType: "MintAttestation",
  typedDataDigest: `0x${"ab".repeat(32)}`,
  signature: `0x${"cd".repeat(65)}`,
  signedAt: "2026-10-16T10:00:00Z",
};
const FIELDS = Object.keys(VALID) as (keyof typeof VALID)[];

describe("parseSignedResult", () => {
  it("returns every required field of a valid file, unchanged", () => {
    expect(parseSignedResult(JSON.stringify(VALID))).toEqual(VALID);
  });

  it("drops fields outside the signed-result schema", () => {
    const parsed = parseSignedResult(
      JSON.stringify({ ...VALID, extra: "ignored", privateKey: "0x1" }),
    );
    expect(parsed).toEqual(VALID);
  });

  it("rejects text that is not JSON", () => {
    expect(() => parseSignedResult("{not json")).toThrow(/not valid JSON/);
  });

  it.each([
    ["an array", "[]"],
    ["null", "null"],
    ["a string", '"signature"'],
    ["a number", "42"],
  ])("rejects %s as not a JSON object", (_name, text) => {
    expect(() => parseSignedResult(text)).toThrow(/must be a JSON object/);
  });

  it.each(FIELDS)("names the field when %s is missing", (field) => {
    const obj: Record<string, unknown> = { ...VALID };
    delete obj[field];
    expect(() => parseSignedResult(JSON.stringify(obj))).toThrow(
      `missing field(s): ${field}.`,
    );
  });

  it.each(FIELDS)("names the field when %s is an empty string", (field) => {
    expect(() =>
      parseSignedResult(JSON.stringify({ ...VALID, [field]: "" })),
    ).toThrow(`missing field(s): ${field}.`);
  });

  it.each([
    ["a number", 1],
    ["null", null],
    ["an object", {}],
    ["a boolean", true],
  ])("rejects a signature that is %s", (_name, value) => {
    expect(() =>
      parseSignedResult(JSON.stringify({ ...VALID, signature: value })),
    ).toThrow("missing field(s): signature.");
  });

  // Hex shape is unchecked here; burns check digests later, while SupplyController verifies mint signatures
  it("accepts hex values of the wrong length without validating them", () => {
    const loose = {
      ...VALID,
      auditor: "0x12",
      typedDataDigest: "0xabc",
      signature: "0x1",
    };
    expect(parseSignedResult(JSON.stringify(loose))).toEqual(loose);
  });

  it("lists every missing field at once", () => {
    expect(() => parseSignedResult("{}")).toThrow(
      "missing field(s): formatVersion, auditor, primaryType, typedDataDigest, signature, signedAt.",
    );
  });
});
