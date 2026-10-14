# Asset `assetSchema` dialect (V1)

This dialect applies to an asset profile's operator-supplied `assetSchema`. Platform envelope
schemas in `shared/schemas/` use Draft 2020-12; see `asset-profile.md`.

The server and offline signer must agree on every asset schema. Both reject unsupported keywords
so one validator cannot silently ignore a rule the other enforces.

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

## Allowlist: annotations

Permitted without validation effects: `title`, `description`, `default`, `examples`, `$comment`,
`$defs`, `$id`, `$schema`, `deprecated`, `readOnly`, `writeOnly`.

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

## Explicitly rejected keywords

Any key outside the allowlists is rejected. Common examples are:

- Composition and conditionals: `oneOf`, `anyOf`, `allOf`, `not`, `if`, `then`, `else`.
- Dynamic: `$dynamicRef`, `$dynamicAnchor`, `$anchor`, `unevaluatedProperties`,
  `unevaluatedItems`.
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
  including rejection of unknown nested fields.
- `limits`: the copy of `shared/schemas/structural-limits.json` checked against each binary's
  hardcoded constants.

Any case disagreement blocks a release. Run the gate with `make dialect-check`.
