# Asset `assetSchema` dialect (V1)

This dialect applies to an asset profile's operator-supplied `assetSchema`. Platform envelope
schemas in `shared/schemas/` use Draft 2020-12; see `asset-profile.md`.

The server and offline signer must agree on asset-schema validation. Both reject
unsupported keywords to avoid silently enforcing different rules.

## Dialect id

`rwa-profile-assetSchema/1`

## Rule

Every schema object may contain only the keys listed below. Any other key MUST reject the schema
at compile time, including unused definitions. Rejection must be deterministic and identical in
both implementations; `shared/vectors/schema-dialect-conformance.json` parity is a release gate.

### Schema position vs. data position

The allowlist applies to schema objects. Values of `enum`, `const`, `default` and `examples` are
opaque data and may contain arbitrary keys. For example, `{"oneOf": 1}` is a valid `const` value.

## Allowlist: validation keywords

- Structural and applicator: `type`, `properties`, `required`, `additionalProperties` (boolean or
  schema), `items`, `$ref`
- Value: `enum`, `const`
- String: `minLength`, `maxLength`, `pattern`
- Number and integer: `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`
- Array: `minItems`, `maxItems`, `uniqueItems`

`type` accepts `object`, `array`, `string`, `integer`, `number`, `boolean`, `null`, or a non-empty
array of unique members of that set. `type: []` is rejected. Omitting `type` permits any type.

Both implementations reject schemas that violate these keyword-value rules during compilation:

- `required` is an array of unique strings.
- `enum` is a non-empty array.
- `minLength`, `maxLength`, `minItems` and `maxItems` are non-negative integers written as plain
  integer literals. `1.0` and `1e1` are rejected even though their values are integers.
- `minimum`, `maximum`, `exclusiveMinimum` and `exclusiveMaximum` are numbers. `exclusiveMinimum:
  true` (the Draft 4 form) is rejected. Literals that overflow float64 or exceed the exact-rational
  parser's exponent limit, such as `1e-999999999`, are rejected.
- `pattern` is a regular expression that compiles under Go's RE2 syntax.

## Allowlist: annotations

Permitted without validation effects: `title`, `description`, `default`, `examples`, `$comment`,
`$defs`, `$id`, `$schema`, `deprecated`, `readOnly`, `writeOnly`.

`title`, `description` and `$comment` are strings, `deprecated`, `readOnly` and `writeOnly` are
booleans, and `examples` is an array. `default` and the members of `examples` are opaque data.

`$schema` and `$id` are ignored and their values are not type-checked. `$schema` cannot select a
different draft: a Draft 4 URI does not change `exclusiveMinimum`, and an unknown URI is neither
fetched nor rejected. `$id` cannot change the base URI; `#/` pointers resolve from the document
root even inside a subschema with its own `$id`. Before compilation, the server removes both
keywords from schema positions. Property and definition names and members of `const` data are
preserved, including those named `$id` or `$schema`.

## `$defs`

Every `$defs` member MUST compile under this dialect, even when no `$ref` reaches it. A
definition containing `multipleOf`, for example, rejects the schema. A definition validates
instance data only when referenced.

## `$ref` rule

- `$ref` MUST be exactly `#` or start with `#/`. Reject remote, relative, absolute and bare-name
  references, including `http…`, `./…` and `urn:…`.
- `$ref` MUST be its object's only member. Validation and annotation siblings are both rejected.
- The pointer MUST resolve within the document. Reject unresolved pointers and direct or indirect
  cycles with bounded depth or a visited set; never recurse without a bound.
- A pointer descends through objects only, so a token that names an array element (`0`, `00`,
  `+0`) never resolves. `~1` stands for `/` and `~0` for `~`; tokens are otherwise taken literally.
  A `$ref` containing `%` is rejected, because the server's compiler percent-decodes the fragment
  and could then read a different definition from the one a literal lookup finds.
- A cycle occurs when expansion reaches a `$ref` string already on its current path, including
  through a target's `$defs`.
- Schema nesting plus `$ref` hops may not exceed depth 100, and a schema may not visit more than
  10,000 nodes, counting each `$ref` expansion as another visit. This bounds work even when
  definitions repeatedly reference the same targets. Both implementations use the same counters.

## Numbers in instances

Validation uses the numeric values represented by the canonical bytes. Each instance number is
rounded to the nearest float64, then read as the exact decimal printed for that float64. Thus
`1.0000000000000000000001` is the integer `1` and `0.10000000000000000001` equals `0.1`. Integer
typing, `minimum` and the other bounds, `enum`, `const` and `uniqueItems` all use that value.
Numbers written in the schema (bounds, `enum` and `const` members) keep their exact decimal
value, so `{"enum": [0.10000000000000000001]}` does not match the instance `0.1`. A number
that overflows float64, in an instance or in a schema keyword, is rejected. Two numbers are
equal when their values are equal, whatever their spelling (`1` and `1.0`).

## Explicitly rejected keywords

Any key outside the allowlists is rejected. Common examples are:

- Composition and conditionals: `oneOf`, `anyOf`, `allOf`, `not`, `if`, `then`, `else`.
- Anchors and dynamic references: `$anchor`, `$dynamicRef`, `$dynamicAnchor`.
- Unevaluated: `unevaluatedProperties`, `unevaluatedItems`.
- Unsupported validation: `multipleOf`, `minProperties`, `maxProperties`, `patternProperties`,
  `propertyNames`, `dependentRequired`, `dependentSchemas`, `contains`, `minContains`,
  `maxContains`, `format`, `prefixItems`, `additionalItems`, `contentEncoding`,
  `contentMediaType`, `contentSchema`.

## Structural limits

Schema documents and asset instances also follow `asset-profile.md`'s structural limits,
including its UTF-8 byte bound on strings and object keys.

## Conformance vectors (differential)

Both binaries run each part of `shared/vectors/schema-dialect-conformance.json`:

- `cases[]`, `{ schema, expect: "accept"|"reject" }`: schema compilation verdict.
- `instanceCases[]`, `{ schema, instance, expectValid: "valid"|"invalid" }`: instance verdict
  after compilation. Compile-only parity does not establish matching asset validation.
- `limitCases[]`, `{ kind, limit }`: construct the documented at-limit input (accept) and
  limit+1 input (reject).
- `envelopeCases[]`, `{ target, document, expectValid }`: profile or metadata envelope verdict,
  including rejection of unknown nested fields, of a key that differs from a field only in case,
  and of non-printable text in a text field.
- `limits`: the copy of `shared/schemas/structural-limits.json` checked against each binary's
  hardcoded constants.

`shared/vectors/jcs-cid.json` adds `surrogateCases`: documents with unpaired `\uD800` to `\uDFFF`
escapes, which both implementations must reject before canonicalizing.

Any case disagreement blocks a release. Run the gate with `make dialect-check`.
