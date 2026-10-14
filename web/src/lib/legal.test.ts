import { describe, expect, it } from "vitest";
import {
  buildLegal,
  EMPTY_LEGAL_FORM,
  legalFromProfileJson,
  safeHttpsHref,
} from "./legal";

const HEX = "ab".repeat(32);

describe("safeHttpsHref", () => {
  it("links https", () => {
    expect(safeHttpsHref("https://issuer.example/t.pdf")).toBe(
      "https://issuer.example/t.pdf",
    );
  });
  it.each([
    "javascript:alert(1)",
    "data:text/html,<b>x</b>",
    "http://issuer.example/t.pdf",
    "ipfs://bafy",
    "not a url",
  ])("renders %s as text", (uri) => {
    expect(safeHttpsHref(uri)).toBeNull();
  });
});

describe("buildLegal", () => {
  it("omits legal when no wrapper is chosen", () => {
    expect(buildLegal({ ...EMPTY_LEGAL_FORM, issuerLei: "x" })).toEqual({});
  });
  it("includes only filled members", () => {
    expect(buildLegal({ ...EMPTY_LEGAL_FORM, wrapper: "share" })).toEqual({
      legal: { wrapper: "share" },
    });
    const { legal } = buildLegal({
      ...EMPTY_LEGAL_FORM,
      wrapper: "claim",
      maturityDate: "2030-12-31",
      redemptionUri: "https://a.example/t",
      redemptionSha256: HEX,
      ratingProvider: "R",
      ratingUri: "https://a.example/r",
      ratingSha256: HEX,
    });
    expect(legal).toEqual({
      wrapper: "claim",
      maturityDate: "2030-12-31",
      redemptionTerms: { uri: "https://a.example/t", sha256: HEX },
      rating: { provider: "R", uri: "https://a.example/r", sha256: HEX },
    });
  });
  it("rejects half a reference and uppercase hex", () => {
    const base = { ...EMPTY_LEGAL_FORM, wrapper: "share" };
    expect(buildLegal({ ...base, redemptionUri: "https://a" }).error).toMatch(
      /both/,
    );
    expect(
      buildLegal({
        ...base,
        redemptionUri: "https://a",
        redemptionSha256: HEX.toUpperCase(),
      }).error,
    ).toMatch(/lowercase/);
    expect(buildLegal({ ...base, ratingGrade: "A" }).error).toMatch(/Rating/);
  });
});

describe("legalFromProfileJson", () => {
  it("reads legal or null", () => {
    expect(legalFromProfileJson('{"legal":{"wrapper":"share"}}')).toEqual({
      wrapper: "share",
    });
    expect(legalFromProfileJson("{}")).toBeNull();
    expect(legalFromProfileJson("nope")).toBeNull();
    expect(legalFromProfileJson(undefined)).toBeNull();
  });
});
