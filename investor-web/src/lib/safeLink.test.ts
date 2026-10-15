import { describe, expect, it } from "vitest";
import { httpsHref } from "./safeLink";

describe("httpsHref", () => {
  it("accepts https", () => {
    expect(httpsHref("https://issuer.example/terms.pdf")).toBe(
      "https://issuer.example/terms.pdf",
    );
  });
  it.each([
    "javascript:alert(1)",
    "data:text/html,<b>x</b>",
    "http://issuer.example/a",
    "ipfs://bafy123",
    "not a url",
    "",
  ])("rejects %s", (uri) => {
    expect(httpsHref(uri)).toBeNull();
  });
});
