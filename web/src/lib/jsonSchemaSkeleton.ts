// Builds a placeholder ("skeleton") instance from an Asset Profile's
// `assetSchema` so the Create-record Metadata editor can start from the shape
// the schema expects instead of an empty `{}`. It walks the schema the same way
// the server's closed dialect (rwa-profile-assetSchema/1) is structured —
// type/properties/items/$ref/$defs/enum/const/default — and emits a
// type-appropriate placeholder for every declared property. It is a best-effort
// authoring aid, not a validator: the server still validates the submitted
// metadata against the real schema.

/** Matches the server's $ref chain bound (assets/schema_guard.go maxRefDepth). */
const MAX_DEPTH = 32;

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Decodes a single JSON Pointer reference token (RFC 6901: ~1→/, ~0→~). */
function decodePointerToken(token: string): string {
  return token.replace(/~1/g, "/").replace(/~0/g, "~");
}

/** Resolves a local JSON-pointer `$ref` (e.g. "#" or "#/$defs/Foo") against the
 * root schema. Only local refs exist in this dialect; anything else → undefined. */
function resolveRef(root: Record<string, unknown>, ref: string): unknown {
  if (ref === "#" || ref === "#/") return root;
  if (!ref.startsWith("#/")) return undefined;
  let node: unknown = root;
  for (const raw of ref.slice(2).split("/")) {
    if (!isObject(node)) return undefined;
    node = node[decodePointerToken(raw)];
  }
  return node;
}

/** Picks the concrete type name to build for, tolerating `type` given as an
 * array (e.g. ["string","null"]) by preferring the first non-null entry. */
function pickType(node: Record<string, unknown>): string | undefined {
  const t = node.type;
  if (typeof t === "string") return t;
  if (Array.isArray(t)) {
    const nonNull = t.find((x) => typeof x === "string" && x !== "null");
    return typeof nonNull === "string"
      ? nonNull
      : typeof t[0] === "string"
        ? t[0]
        : undefined;
  }
  return undefined;
}

function skeletonForSchema(
  node: unknown,
  root: Record<string, unknown>,
  depth: number,
  seenRefs: ReadonlySet<string>,
): unknown {
  if (depth > MAX_DEPTH || !isObject(node)) return null;

  // Resolve a $ref before anything else, guarding against reference cycles.
  if (typeof node.$ref === "string") {
    if (seenRefs.has(node.$ref)) return null;
    const target = resolveRef(root, node.$ref);
    return skeletonForSchema(
      target,
      root,
      depth + 1,
      new Set(seenRefs).add(node.$ref),
    );
  }

  // Explicit authoring hints win over a synthesized placeholder.
  if ("default" in node) return node.default;
  if ("const" in node) return node.const;
  if (Array.isArray(node.enum) && node.enum.length > 0) return node.enum[0];
  if (Array.isArray(node.examples) && node.examples.length > 0)
    return node.examples[0];

  const type = pickType(node);

  // An object (declared as such, or implied by having `properties`): emit a
  // placeholder for every declared property so the operator sees the full shape.
  if (type === "object" || (type === undefined && isObject(node.properties))) {
    const props = isObject(node.properties) ? node.properties : {};
    const out: Record<string, unknown> = {};
    for (const [key, sub] of Object.entries(props)) {
      out[key] = skeletonForSchema(sub, root, depth + 1, seenRefs);
    }
    return out;
  }

  if (type === "array") {
    return isObject(node.items)
      ? [skeletonForSchema(node.items, root, depth + 1, seenRefs)]
      : [];
  }

  switch (type) {
    case "string":
      return "";
    case "integer":
    case "number":
      return 0;
    case "boolean":
      return false;
    case "null":
      return null;
    default:
      return null;
  }
}

/**
 * Returns a pretty-printed JSON skeleton for a profile's `assetSchema`, or
 * `undefined` if the schema is not a usable object schema (so the caller can
 * fall back to an empty template).
 */
export function buildMetadataSkeleton(schema: unknown): string | undefined {
  if (!isObject(schema)) return undefined;
  const value = skeletonForSchema(schema, schema, 0, new Set<string>());
  if (!isObject(value)) return undefined;
  return JSON.stringify(value, null, 2);
}
