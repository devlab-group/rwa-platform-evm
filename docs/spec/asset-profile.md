# Asset profile and metadata limits

An asset profile defines the project's asset class; metadata describes an issuance record.
Their envelopes, `shared/schemas/asset-profile.schema.json` and
`shared/schemas/metadata.schema.json`, use Draft 2020-12 with UUID and date-time formats.
The signer and server MUST enforce matching limits and derivations. Other implementations,
including TypeScript clients, MUST reproduce the shared vectors.

## Documents

Profile fields: `profileVersion` (`"1.0"`), UUID `projectId` (the server requires its configured
`contract.project_id`), `assetType` (1 to 128 characters), `tokenUnit` (1 to 64), integer
`tokenDecimals` (0 to 36), `recordIdLabel` (1 to 128), optional `displayFields[]` (`label` and
`pointer` starting with `/`), optional `legal` (see "Legal descriptor"), and `assetSchema`. The
`assetType`, `tokenUnit` and `recordIdLabel` lengths count Unicode code points, as JSON Schema
`maxLength` does; the byte-based string bound under "Structural limits" also applies.

Metadata fields: `platformVersion` (`"1.0"`), UUID `projectId`, `recordId`
(`^[A-Za-z0-9._:-]{1,128}$`), `asset` validated against `assetSchema`, `issuance` (`amount`, a
positive base-unit decimal string at most 2^256-1; non-empty `unit`), optional `proofs[]` (`type`,
64-character lowercase hex `sha256`, optional `uri`), and RFC 3339 `createdAt`.

Unknown envelope fields reject the document, including inside `displayFields[]`, `legal`,
`issuance` and `proofs[]`. The operator's `assetSchema` follows the closed dialect in
`profile-schema-dialect.md`, rather than full Draft 2020-12.

## Legal descriptor

The optional `legal` object describes the instrument the token represents. It is fixed with the
profile for the deployment and bound into every attestation through `profileDigest`. The platform
records the issuer's declarations without verifying them, resolving the LEI or acting on
`maturityDate`.

| Field             | Required | Rule                                                                                                                                    |
| ----------------- | -------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `wrapper`         | yes      | `share`, `entitlement`, `certificate` or `claim`                                                                                        |
| `issuerLei`       | no       | ISO 17442 LEI: `^[A-Z0-9]{18}[0-9]{2}$` passing ISO 7064 MOD 97-10 (expand A-Z to 10-35; the resulting decimal number mod 97 must be 1) |
| `holderRights`    | no       | text, 1 to 2,048 characters                                                                                                             |
| `redemptionTerms` | no       | document reference                                                                                                                      |
| `maturityDate`    | no       | RFC 3339 `full-date` that is a real calendar date                                                                                       |
| `rating`          | no       | `provider` (text, 1 to 128 characters), optional `grade` (text, 1 to 32), and a document reference's `uri` and `sha256`                 |

A document reference is `{uri, sha256}`: `uri` is 1 to 2,048 characters with no whitespace;
`sha256` is the document's SHA-256 as 64 lowercase hex characters. All `legal` objects reject
unknown fields. Text must pass Go `unicode.IsPrint` and contain no C0, DEL or C1 controls,
rejecting tabs, newlines, non-ASCII spaces and bidi formatting characters. Lengths count Unicode
code points.

The public `GET /api/v1/project` returns `legal` when the stored profile has one. Clients MUST
link a reference only when its URI parses as an `https:` URL and show other values as text.
The admin console and investor app follow this rule, keeping `javascript:` and `data:` URIs
inactive.

## Structural limits

These limits apply to profile, metadata and `assetSchema` documents and their asset data, even
when the asset schema specifies no limits. `shared/schemas/structural-limits.json` defines them:

| Limit                           | Key                    | Value                   |
| ------------------------------- | ---------------------- | ----------------------- |
| Max document size (bytes)       | `maxUncompressedBytes` | 1,048,576 (1 MiB)       |
| Max object/array nesting depth  | `maxDepth`             | 32                      |
| Max string length (UTF-8 bytes) | `maxStringLength`      | 32,768                  |
| Max array length                | `maxArrayItems`        | 4,096                   |
| Max object keys                 | `maxObjectProperties`  | 4,096                   |
| Encoding                        |                        | valid UTF-8 only        |
| Duplicate JSON object keys      |                        | forbidden (parse error) |

Each binary hardcodes the limits and tests them against the `limits` copy in
`shared/vectors/schema-dialect-conformance.json`. Differences can make a package pass one
validator and fail the other.

The string bound counts UTF-8 bytes, including object keys, rather than code points or UTF-16
units. A 32,769-byte key fails; so does a shorter sequence of multibyte characters whose encoding
exceeds 32,768 bytes. The byte bound limits parsing resources. Its implementations are
`server/internal/auditpkg/limits.go` and `signer/internal/canonical/canonical.go`.

## Numeric rules

- Values affecting on-chain amounts MUST be JSON strings, including `issuance.amount`, decimal
  weights and prices, so they retain their exact value.
- JSON integers MUST remain within [-(2^53-1), 2^53-1] = [-9007199254740991, 9007199254740991].
  Values outside this cross-language safe range MUST be strings.

The server and the signer both reject an out-of-range plain integer literal anywhere in a
profile or metadata document. Neither checks a literal written with a fraction or exponent (such
as `1e20`), so package producers must follow the rule for those themselves.

## Derivation conventions

- `metadataDigest` / `profileDigest` = `SHA-256(RFC 8785 canonical bytes)`, as 0x-hex.
  `shared/vectors/jcs-cid.json` pins canonical bytes, SHA-256 and CIDv1.
- CIDv1 is base32-lower multibase of `0x01` (v1), `0x55` (raw), `0x12` (sha2-256), `0x20`
  (length), followed by the digest.
- `recordKey` = `keccak256(utf8(recordId))`.
- On-chain `ProjectConfig.projectId` (bytes32) = `keccak256(utf8(profile.projectId))`.
  The UUID remains the off-chain identity; the hash binds it on-chain.

## Webhook authentication

`POST /api/v1/compliance/webhook` authenticates the configured provider's deliveries:

| `kyc.provider`   | Header                | Value                                                                                        |
| ---------------- | --------------------- | -------------------------------------------------------------------------------------------- |
| `none` (generic) | `X-Webhook-Signature` | hex `HMAC-SHA256(rawBody, sharedSecret)`, optionally `0x`-prefixed                           |
| `sumsub`         | `X-Payload-Digest`    | hex HMAC of the raw body; `X-Payload-Digest-Alg` selects SHA-256 (default), SHA-1 or SHA-512 |
| `onfido`         | `X-SHA2-Signature`    | hex HMAC-SHA256 of the raw body                                                              |

Signature comparison is constant time. Decisions require a unique provider event ID, a timestamp
no older than 24 hours and no more than five minutes ahead, and prior wallet-ownership proof.
The server MUST NOT act on an unproved wallet from a webhook; that path has no override. The
admin API's manual allow or block is the documented alternative.

Walletless Onfido watchlist-monitor signals raise an alert without entering the decision replay
or freshness checks. Repeated deliveries can therefore repeat that alert; they change no
on-chain status.
