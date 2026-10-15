import type { components } from "./api-types";

type SignedResult = components["schemas"]["SignedResult"];

// Required fields from shared/schemas/signed-result.schema.json
const SIGNED_RESULT_FIELDS = [
  "formatVersion",
  "auditor",
  "primaryType",
  "typedDataDigest",
  "signature",
  "signedAt",
] as const;

/** Requires non-empty signed-result.json fields; does not verify the signature */
export function parseSignedResult(text: string): SignedResult {
  let obj: unknown;
  try {
    obj = JSON.parse(text);
  } catch {
    throw new Error("The selected file is not valid JSON.");
  }
  if (typeof obj !== "object" || obj === null || Array.isArray(obj)) {
    throw new Error("signed-result.json must be a JSON object.");
  }
  const rec = obj as Record<string, unknown>;
  const missing = SIGNED_RESULT_FIELDS.filter(
    (k) => typeof rec[k] !== "string" || rec[k] === "",
  );
  if (missing.length > 0) {
    throw new Error(
      `Not a signed-result.json - missing field(s): ${missing.join(", ")}.`,
    );
  }
  return {
    formatVersion: String(rec.formatVersion),
    auditor: String(rec.auditor),
    primaryType: String(rec.primaryType),
    typedDataDigest: String(rec.typedDataDigest),
    signature: String(rec.signature),
    signedAt: String(rec.signedAt),
  };
}
