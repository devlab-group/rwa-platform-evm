// Builds assetSchema authoring placeholders from type/properties/items, local $ref/$defs
// and default/const/enum/examples hints. Explicit hints can replace the declared object shape.
// The server validates the rwa-profile-assetSchema/1 dialect; this helper is not a validator.

// Bounds placeholder recursion; the server validates reference chains separately
const MAX_DEPTH = 32;

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

// Decodes JSON Pointer tokens: ~1 to / and ~0 to ~ (RFC 6901)
function decodePointerToken(token: string): string {
  return token.replace(/~1/g, "/").replace(/~0/g, "~");
}

// Resolve local JSON Pointers (# or #/$defs/Foo) against the root; unsupported references return
// undefined
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

// Choose the first non-null type from an array, falling back to its first string entry
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

  // Resolve a $ref first, guarding against reference cycles
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

  // Explicit hints win over a synthesized placeholder
  if ("default" in node) return node.default;
  if ("const" in node) return node.const;
  if (Array.isArray(node.enum) && node.enum.length > 0) return node.enum[0];
  if (Array.isArray(node.examples) && node.examples.length > 0)
    return node.examples[0];

  const type = pickType(node);

  // Populate declared object properties unless explicit hints supplied a value
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

/** Returns formatted object-schema placeholders, or undefined so callers can use an empty template */
export function buildMetadataSkeleton(schema: unknown): string | undefined {
  if (!isObject(schema)) return undefined;
  const value = skeletonForSchema(schema, schema, 0, new Set<string>());
  if (!isObject(value)) return undefined;
  return JSON.stringify(value, null, 2);
}
