# Asset `assetSchema` dialect (V1)

Governs the **operator-supplied `assetSchema`** field of an Asset
Profile only. It does **not** govern the platform's own JSON Schemas in
`shared/schemas/` (those are platform-controlled and validated with a full
Draft 2020-12 validator; e.g. `asset-profile.schema.json` legitimately uses
`format: uuid`).

The reason it exists: the server compiled operator `assetSchema`
documents with a full Draft 2020-12 implementation while the offline signer
implemented a hand-written subset that **silently ignored** unknown keywords.
A schema using e.g. `multipleOf`, `format`, or `patternProperties` was therefore
enforced by the server but ignored by the signer — a real divergence. Both binaries
now enforce this single closed dialect and reject anything outside it.

## Dialect id

`rwa-profile-assetSchema/1`

## Rule

Every **schema object** in the `assetSchema` tree may contain only keys drawn
from the **allowlist** below. Any key that is neither a supported validation
keyword nor an allowed annotation MUST cause the schema to be **rejected at
compile/guard time**, even if it appears only as an unused sibling. Rejection
is deterministic and identical in the server and the signer; validator parity
against `shared/vectors/schema-dialect-conformance.json` is a **release gate**.

### Schema position vs. data position

The allowlist is enforced on **schema objects only**. The *values* of `enum`,
`const`, `default`, and `examples` are opaque data, not schemas — they are NOT
walked and may contain any keys (e.g. a `const` whose value is
`{"oneOf": 1}` is data and is accepted).

## Allowlist — supported validation keywords (enforced identically)

- Structural/applicator: `type`, `properties`, `required`,
  `additionalProperties` (boolean or schema), `items`, `$ref`
- Value: `enum`, `const`
- String: `minLength`, `maxLength`, `pattern`
- Number/integer: `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`
- Array: `minItems`, `maxItems`, `uniqueItems`

`type` values are restricted to: `object`, `array`, `string`, `integer`,
`number`, `boolean`, `null` (string or array-of-strings). Absent `type` = any.

## Allowlist — annotations (permitted, ignored for validation)

`title`, `description`, `default`, `examples`, `$comment`, `$defs`, `$id`,
`$schema`, `deprecated`, `readOnly`, `writeOnly`.

`$defs` is a container: its members are schema objects, compiled only when
reached via `$ref`, and each MUST itself satisfy this dialect.

## `$ref` rule

- `$ref` MUST be a local JSON pointer: exactly `#` or a string starting `#/`.
  Any remote/relative/absolute reference (`http…`, `./…`, `urn:…`, a bare name)
  is **rejected**.
- `$ref` MUST be the **sole member** of its object. A `$ref` accompanied by any
  other key (validation keyword OR annotation) is **rejected**. (This removes
  the Draft 2020-12 `$ref`-with-siblings ambiguity that the two validators
  resolved differently.)
- The pointer MUST resolve within the same document; an unresolvable pointer is
  rejected. Reference cycles (direct or indirect) MUST be detected and rejected
  deterministically with a bounded depth/visited set, never
  by unbounded recursion.

## Explicitly rejected keywords (non-exhaustive; the closed allowlist is authoritative)

Composition/conditional: `oneOf`, `anyOf`, `allOf`, `not`, `if`, `then`, `else`.
Dynamic: `$dynamicRef`, `$dynamicAnchor`, `$anchor`,
`unevaluatedProperties`, `unevaluatedItems`.
Unsupported validation: `multipleOf`, `minProperties`, `maxProperties`,
`patternProperties`, `propertyNames`, `dependentRequired`, `dependentSchemas`,
`contains`, `minContains`, `maxContains`, `format`, `prefixItems`,
`additionalItems`, `contentEncoding`, `contentMediaType`, `contentSchema`.

Anything not on the allowlist is rejected regardless of whether it is listed
here.

## Structural limits

Both the server and the signer MUST enforce **exactly** these limits, from
`shared/schemas/structural-limits.json`, when parsing/validating any operator
profile, metadata, or assetSchema JSON (and the asset-data instances validated
against an assetSchema):

| limit | value |
| --- | ---: |
| max uncompressed bytes | 1,048,576 (1 MiB) |
| max nesting depth | 32 |
| max string length (UTF-8 bytes) | 32,768 |
| max array items | 4,096 |
| max object properties | 4,096 |

Each binary hardcodes these and asserts equality against
`shared/schemas/structural-limits.json` in a test. Divergent limits let a
hand-built package pass one validator and fail the other.

**Max string length unit:** measured in UTF-8 bytes
(Go `len(string)`), not Unicode runes/scalar values and not UTF-16 code
units, and applies identically to string values AND object property keys.
See docs/spec/asset-profile.md's "Max string length unit" note. An earlier
version of the signer measured string VALUES in runes and never bounded object
KEYS at all, so a 10,000-emoji value (10,000 runes, ~40,000 UTF-8 bytes) or
a 32,769-byte object key that the server rejected was silently accepted by
the signer — the byte-based rule closes that gap.

## `$defs` are recursively validated

Every member of a `$defs` object is a schema and MUST satisfy this dialect at
compile time **even if it is never reached through `$ref`**. A `$defs` entry
containing a forbidden keyword (e.g. `multipleOf`) is rejected. (The signer
previously ignored unreferenced `$defs`; it no longer does.)

## `type` must be non-empty

`type` is a string or a **non-empty** array of unique valid type strings. An
empty `type: []` is rejected (it must not silently compile to "any type").

## Strict document envelopes

Profile and metadata documents (and their nested `displayFields[]`,
`issuance`, and `proofs[]` objects) MUST be decoded strictly on both sides:
an unknown field anywhere in the envelope is rejected, not ignored. The server
previously unmarshalled nested envelope objects loosely; it now rejects unknown
fields to match the signer.

## Conformance vectors (differential)

`shared/vectors/schema-dialect-conformance.json` is a **differential** corpus,
not compile-only. Both binaries run every array and must agree:

- `cases[]` — `{ schema, expect: "accept"|"reject" }`: does the assetSchema
  **compile** under the dialect.
- `instanceCases[]` — `{ schema, instance, expectValid: "valid"|"invalid" }`:
  the schema compiles; both validators must **validate the instance** to the
  same verdict. (Compile-only parity is insufficient — this is the security
  boundary: the assetSchema validates the asset data the signature attests.)
- `limitCases[]` — `{ kind, limit }`: each binary constructs the same at-limit
  input (accept) and limit+1 input (reject) per the documented construction
  rule and asserts the structural limit above.
- `envelopeCases[]` — `{ target, document, expectValid }`: strict-envelope
  decoding of a full profile/metadata document (unknown nested field ⇒
  invalid).

Any disagreement between the two real binaries on any case is a release-blocking
failure (gated by `make dialect-check`).
