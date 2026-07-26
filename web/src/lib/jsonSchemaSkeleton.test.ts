import { describe, expect, it } from "vitest";
import { buildMetadataSkeleton } from "./jsonSchemaSkeleton";

const parse = (s: string | undefined) => (s === undefined ? undefined : JSON.parse(s));

describe("buildMetadataSkeleton", () => {
  it("emits a type-appropriate placeholder for each declared property", () => {
    const schema = {
      type: "object",
      properties: {
        serial: { type: "string" },
        weightGrams: { type: "number" },
        purity: { type: "integer" },
        insured: { type: "boolean" },
      },
      required: ["serial"],
    };
    expect(parse(buildMetadataSkeleton(schema))).toEqual({
      serial: "",
      weightGrams: 0,
      purity: 0,
      insured: false,
    });
  });

  it("includes all declared properties, not just required ones", () => {
    const schema = {
      type: "object",
      properties: { a: { type: "string" }, b: { type: "string" } },
      required: ["a"],
    };
    expect(parse(buildMetadataSkeleton(schema))).toEqual({ a: "", b: "" });
  });

  it("recurses into nested objects and arrays", () => {
    const schema = {
      type: "object",
      properties: {
        custodian: {
          type: "object",
          properties: { name: { type: "string" }, id: { type: "integer" } },
        },
        tags: { type: "array", items: { type: "string" } },
        owners: {
          type: "array",
          items: {
            type: "object",
            properties: { address: { type: "string" } },
          },
        },
      },
    };
    expect(parse(buildMetadataSkeleton(schema))).toEqual({
      custodian: { name: "", id: 0 },
      tags: [""],
      owners: [{ address: "" }],
    });
  });

  it("prefers default, then const, then enum/examples", () => {
    const schema = {
      type: "object",
      properties: {
        withDefault: { type: "string", default: "gold" },
        withConst: { const: "FIXED" },
        withEnum: { type: "string", enum: ["a", "b"] },
        withExamples: { type: "string", examples: ["ex1"] },
      },
    };
    expect(parse(buildMetadataSkeleton(schema))).toEqual({
      withDefault: "gold",
      withConst: "FIXED",
      withEnum: "a",
      withExamples: "ex1",
    });
  });

  it("resolves local $ref against $defs", () => {
    const schema = {
      type: "object",
      properties: { primary: { $ref: "#/$defs/party" } },
      $defs: {
        party: {
          type: "object",
          properties: { name: { type: "string" } },
        },
      },
    };
    expect(parse(buildMetadataSkeleton(schema))).toEqual({
      primary: { name: "" },
    });
  });

  it("terminates on a self-referential $ref cycle instead of recursing forever", () => {
    const schema = {
      type: "object",
      properties: { node: { $ref: "#/$defs/node" } },
      $defs: {
        node: {
          type: "object",
          properties: {
            value: { type: "string" },
            child: { $ref: "#/$defs/node" },
          },
        },
      },
    };
    // The cycle guard stops the second visit to #/$defs/node → child becomes null.
    expect(parse(buildMetadataSkeleton(schema))).toEqual({
      node: { value: "", child: null },
    });
  });

  it("treats a schema with properties but no explicit type as an object", () => {
    const schema = { properties: { x: { type: "string" } } };
    expect(parse(buildMetadataSkeleton(schema))).toEqual({ x: "" });
  });

  it("returns an empty object for an object schema with no properties", () => {
    expect(parse(buildMetadataSkeleton({ type: "object" }))).toEqual({});
  });

  it("returns undefined for a non-object / unusable schema", () => {
    expect(buildMetadataSkeleton(null)).toBeUndefined();
    expect(buildMetadataSkeleton("nope")).toBeUndefined();
    expect(buildMetadataSkeleton({ type: "string" })).toBeUndefined();
  });
});
