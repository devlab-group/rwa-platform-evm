# Asset Profile & metadata limits

Schema: `shared/schemas/asset-profile.schema.json`, `shared/schemas/metadata.schema.json`.
A Draft 2020-12 subset. This file pins down the cross-language limits and derivation conventions
the signer, server, and TS client MUST all enforce identically.

## JSON Schema subset (operator `assetSchema`)

Allowed keywords: `type`, `properties`, `required`, `additionalProperties`, `items`,
`title`, `description`, `default`, `enum`, `const`, `minLength`, `maxLength`, `pattern`,
`minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `minItems`, `maxItems`,
`uniqueItems`, and local `$ref` (`#/...` within the bundled profile only).

Rejected (validation error, not ignored): `oneOf`, `anyOf`, `allOf`, `not`,
`if`/`then`/`else`, `$dynamicRef`, `$dynamicAnchor`, `unevaluatedProperties`,
`unevaluatedItems`, recursive schemas, and any remote/network `$ref`.

## Structural limits (enforced even when the schema omits them)

| Limit                         | Value                     |
| ----------------------------- | ------------------------- |
| Max document size (bytes)     | 1,048,576 (1 MiB)         |
| Max object/array nesting depth| 32                        |
| Max string length (UTF-8 bytes) | 32,768                  |
| Max array length              | 4,096                     |
| Max object keys               | 4,096                     |
| Encoding                      | valid UTF-8 only          |
| Duplicate JSON object keys    | forbidden (parse error)   |

**Max string length unit:** the 32,768 bound is measured
in **UTF-8 bytes**, i.e. Go `len(string)`/the length of the value's UTF-8
encoding -- NOT Unicode scalar values/runes and NOT UTF-16 code units. It
applies IDENTICALLY to every JSON string token in the document, including
**object property keys**, not only string values. This was chosen (over a
rune/codepoint count) because it is the cheaper, DoS-relevant bound and
because it is what the server already enforced; the offline signer MUST
enforce the exact same byte-length check on both keys and values (see
`shared/schemas/structural-limits.json`, `signer/internal/canonical/canonical.go`,
`server/internal/auditpkg/limits.go`). A document with a string value or
object key whose UTF-8 encoding is longer than 32,768 bytes is rejected even
if its rune/codepoint count is at or below 32,768 (e.g. a 32,769-byte object
key, or a value made of enough multi-byte characters to exceed 32,768 bytes
while having far fewer than 32,768 runes).

## Numeric rules

- Every value that affects an on-chain amount MUST be a JSON **string**, never a JSON number
  (e.g. `issuance.amount`, decimal weights/prices). Contracts and signer treat these as exact.
- JSON integers that DO appear MUST stay within the cross-language safe range
  **[-(2^53-1), 2^53-1]** = [-9007199254740991, 9007199254740991] so Go/TS/JSON agree. Values
  outside this range MUST be strings.

## Derivation conventions

- `metadataDigest` / `profileDigest` = `SHA-256(RFC 8785 canonical bytes)`, 0x-hex. Golden
  reference: `shared/vectors/jcs-cid.json` (canonical bytes → sha256 → CIDv1 raw/sha2-256).
- CIDv1 = multibase base32-lower, `0x01`(v1) `0x55`(raw) `0x12`(sha2-256) `0x20`(len) ‖ digest.
- `recordKey` = `keccak256(utf8(recordId))`.
- On-chain `ProjectConfig.projectId` (bytes32) = `keccak256(utf8(profile.projectId))` where
  `profile.projectId` is the UUID string. The UUID string remains the off-chain identity;
  the bytes32 is its on-chain binding.

## Webhook authentication

- KYC webhook HMAC signature header: **`X-Webhook-Signature`**, value = lowercase hex of
  `HMAC-SHA256(rawBody, sharedSecret)`. Verify with a constant-time compare. Replay-protected
  via a per-event id + timestamp window. The server MUST NOT accept a wallet address from a
  webhook unless that wallet previously proved ownership, or a documented manual override is used.
