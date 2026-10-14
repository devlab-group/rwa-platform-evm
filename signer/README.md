# signer: air-gapped EIP-712 auditor signer

`signer` reviews a `.rwa` audit package offline and creates a `signed-result.json` authorizing
one mint or burn on `SupplyController`. For a Safe auditor, each owner signs a share and
`signer combine` assembles the final result. The signing commands make no network requests.

Start with the [auditor guide](../docs/auditor/auditor-guide.md) for machine hygiene, evidence
review and key management. This README describes validation, formats and tests. The shared
interfaces are `shared/eip712/types.md`, `shared/schemas/*.json` and `shared/vectors/*.json`.

## What it does, end to end

```
signer sign <package.rwa> --keystore <path> --policy <path-to-policy.json>
```

Every signing run requires a local `--policy` maintained independently of the package. Its six
deployment fields are `chainId`, `controller`, `vault`, `auditor`, `projectId` and
`profileDigest`; lifetime and Safe settings are optional. See "The `--policy` flag" below.

1. Read the compressed package with `rwapkg.ReadPackageFile`. Check its size with `Stat`, then
   enforce the bound with a limited reader. Extract the ZIP in memory without writing entries
   to disk. Reject traversal (any name that `path.Clean` would change, such as `../`, `./` or
   a trailing slash, plus absolute paths and backslashes), symlinks indicated by either decoded
   file mode or raw Unix attributes, duplicate names, unsupported compression methods and
   excessive entry counts. Only Store and Deflate are accepted. Decompression limits count
   bytes actually read across the archive, regardless of sizes claimed in ZIP headers.
2. Parse the manifest with `rwapkg.ParseManifest`, checking the
   `shared/schemas/rwa-manifest.schema.json` envelope. Each mandatory document must appear once,
   file paths must be unique, and other payloads must live under `proofs/`. Verify every listed
   file's size and SHA-256 and reject undeclared payloads; the manifest itself is not a payload
   entry in its own inventory.
3. Validate the profile envelope in `internal/profile`: fixed `profileVersion`, UUID
   `projectId`, string bounds, `displayFields` shape and no unknown fields, including in nested
   display-field objects. Compile `assetSchema` with `internal/jsonschema` using the pinned
   `rwa-profile-assetSchema/1` dialect (`docs/spec/profile-schema-dialect.md`). Its keywords are:

   - validation: `type`, `properties`, `required`, `additionalProperties`, `items`, `$ref`, `enum`,
     `const`, `minLength`, `maxLength`, `pattern`, `minimum`, `maximum`, `exclusiveMinimum`,
     `exclusiveMaximum`, `minItems`, `maxItems`, `uniqueItems`;
   - annotations and definition containers: `$schema`, `$id`, `$comment`, `$defs`, `title`,
     `description`, `default`, `examples`, `deprecated`, `readOnly`, `writeOnly`.

   `type` accepts any of the seven JSON types or a non-empty array of unique types. Local `$ref`
   values (`#` or `#/...`) must have no sibling keywords; resolution has cycle and depth bounds.
   Unsupported keywords, including `multipleOf`, `format` and `patternProperties`, are rejected
   in compiled schema nodes, including every `$defs` member even when no reference reaches it.
   The canonical parser used for profiles, metadata and schema instances defaults to 1 MiB,
   depth 32, 32768 UTF-8 bytes per string or key, 4096 array items and 4096 object properties.
   `TestDialectStructuralLimitsMatchSharedConstants` checks these values
   against `shared/schemas/structural-limits.json` and the copy in
   `shared/vectors/schema-dialect-conformance.json`.
4. Validate metadata in `internal/metadata`: `recordId` matches `^[A-Za-z0-9._:-]{1,128}$`,
   `issuance.amount` is a positive decimal string at most 2²⁵⁶ − 1, proof hashes are lowercase
   hex, and `createdAt` is RFC 3339. Unknown fields are rejected, including in `issuance` and
   proof objects. Validate `asset` against the compiled profile schema with
   `Profile.ValidateAsset`. Require the metadata, profile and policy project IDs to agree and
   `issuance.unit` to equal `profile.tokenUnit`.
5. Canonicalize the raw profile and metadata with RFC 8785 JCS and recompute their SHA-256
   digests. The profile digest must match the policy and manifest; the metadata digest must
   match the manifest. `internal/canonical` rejects duplicate keys and invalid UTF-8, enforces
   configurable structural limits, sorts keys by UTF-16 code units, formats numbers using
   ECMAScript `Number::toString`, and escapes strings minimally. `internal/cid` derives CIDv1
   with codec `raw` (0x55) and multihash sha2-256 from the same bytes. Signing does not call it;
   the vector tests do. Its vectors were generated with
   `ipfs add --cid-version=1 --raw-leaves --hash=sha2-256 --only-hash`.
6. Parse and cross-check typed data (`cmd/signer/typeddata.go`; format:
   `shared/schemas/typed-data.schema.json`). Digests and `primaryType` must match the package.
   Compare amounts as `big.Int`, so leading-zero decimal representations are equivalent. For
   a mint, check `recordId` and `recordKey = keccak256(bytes(recordId))`.
7. Check chain ID, controller, Vault and auditor against the policy. Require
   `now < validUntil <= min(now, metadata.createdAt) + maxAttestationLifetime`, with a 30-day
   default. Reject mismatches and invalid deadlines before review, then compute the EIP-712
   digest in `internal/attestation`.
8. Render the review screen in `internal/ui`. Show the mint or burn banner, `primaryType`, both
   project IDs, unit, decimals, chain, controller, auditor, Vault, digests, raw and normalized
   amounts, nonce, deadline and policy window, EIP-712 digest, and policy path. Mint review also
   shows `recordId` and `recordKey`; burn review shows `operationId`. Safe mode adds its auditor
   type, threshold, owners and message hash. Resolve each `displayFields` JSON pointer (RFC 6901) against
   the metadata's `asset`, using "(not present)" when it cannot resolve. Rendering happens after
   digest computation; the `displayFields` definitions themselves are validated and included in
   the profile digest. List each proof with a matching attached file or
   `NO ATTACHED FILE MATCHES THIS HASH`. Sanitize screen titles, labels and values with
   `textsafe.SanitizeForTerminal` to neutralize control, ANSI and bidi characters. Parsing
   already rejects non-printable text in `assetType`, `tokenUnit`, `recordIdLabel`,
   display-field labels, `issuance.unit` and proof types.
9. Read confirmation from stdin with `ui.Confirm`. Only `y` or `yes`, ignoring case and surrounding
   whitespace, proceeds. Blank or other responses decline; EOF with no response declines too.
   `--yes` skips confirmation only with `--unsafe-test-mode`. Both flags are for tests and CI;
   they do not bypass policy validation.
10. Unlock the key and sign with go-ethereum's `crypto.Sign`, using deterministic RFC 6979
    nonces. The EOA path signs the raw EIP-712 digest and produces 65-byte `[R‖S‖V]`, with `V`
    adjusted to 27 or 28. Check the loaded key's address against the declared auditor after
    signing. A mismatch prevents output but can leave unlock and signature audit entries.
11. Write `signed-result.json`, defaulting to `<package-dir>/signed-result.json`; `--out` changes
    the path. `SignedResult.Validate` checks field formats corresponding to the shared schema.

Safe signing replaces steps 10 and 11 as described below. Errors return a non-zero status.
Validation, confirmation and key-identity failures occur before output; an output-write error
can leave a partial file. Audit, anchor and throttle files have their own side effects and error
rules under "Keys and secrets".

## Module layout

```
signer/
├── cmd/signer/           CLI entry point (package main, binary "signer")
│   ├── main.go             flag parsing, the pipeline above, and "signer combine"
│   ├── keystore_cmd.go     "signer keystore create"/"import" subcommands
│   ├── typeddata.go        typed-data.json parsing and validation
│   └── review.go           JSON-pointer resolution and review-screen field building
└── internal/
    ├── canonical/          RFC 8785 JCS: parser and canonical serializer
    ├── cid/                CIDv1 (raw, sha2-256) from canonical bytes (vector tests only)
    ├── jsonschema/         operator assetSchema dialect validator (docs/spec/profile-schema-dialect.md)
    ├── profile/            Asset Profile envelope validation and assetSchema compilation
    ├── metadata/           metadata envelope validation
    ├── package/            .rwa ZIP extraction and manifest verification (Go package name: rwapkg)
    ├── policy/             --policy file loading: the auditor's independent trust root
    ├── attestation/        EIP-712 domain, type hashes, digest, Safe message hash, Sign, Recover
    ├── keystore/           keystore load and create, unlock hardening (see "Keys and secrets")
    ├── hardware/           Signer interface, DeviceKind/Open factory, non-production StubAdapter
    ├── output/             signed-result.json and Safe signature shares, with format checks
    ├── textsafe/           neutralizes untrusted text for the review terminal; printable-text check for validators
    └── ui/                 review-screen rendering, mint/burn banner, fail-closed confirm prompt
```

Import `internal/package` as `rwapkg`; `package` is a reserved Go word.

## EIP-712 construction and vector reproduction

`internal/attestation` uses the canonical type strings in `shared/eip712/types.md`:

```
EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)
MintAttestation(address auditor,bytes32 profileDigest,bytes32 recordKey,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)
BurnAttestation(address auditor,bytes32 profileDigest,bytes32 operationId,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)
```

Both attestations contain only EVM value types (`address`, `bytesN`, `uintN`). Their
`abi.encode(...)` representation is a sequence of 32-byte words, built directly with
`common.LeftPadBytes` and `crypto.Keccak256Hash` without a general ABI encoder. `validUntil`
occupies a full word even though its type is `uint64`. Profile and metadata digests use SHA-256;
EIP-712 hashes and the record key use keccak256.

The shared vectors pin the following results:

- `TestVectors` in `internal/attestation/vectors_test.go` reproduces the frozen domain and
  attestation type hashes from `shared/vectors/typehashes.json`, then the domain separator,
  struct hash, digest and exact deterministic signature from `shared/vectors/mint-eip712.json`
  using its `signerPrivateKey`.
- `TestVectors_SafeAuditor` reproduces the Safe hashes, owner signatures and concatenated result
  from `shared/vectors/safe-auditor.json` (see below).
- `internal/canonical/jcscid_vector_test.go` reproduces canonical bytes, SHA-256 and CIDv1 from
  `shared/vectors/jcs-cid.json`. The canonical serializer's tests also cover UTF-16 key ordering
  for supplementary characters.

## Keys and secrets

`internal/keystore.Load` returns a private key and its address. It detects Ethereum V3 JSON
keystores (`keystore.DecryptKey`) or the native `rwa-argon2id-v1` format, which uses Argon2id and
AES-256-GCM. Defer `keystore.Zero` immediately after loading, as the CLI does. It overwrites the
key's `D` scalar, but cannot guarantee removal of every copy held by Go or a library.

Before decryption, `Load` rejects any group or other permission bits and checks the file's size
against `MaxKeystoreFileBytes` (1 MiB). It rechecks the length after reading to detect growth.
Both formats enforce lower and upper KDF bounds before key derivation:

| KDF | Accepted parameters |
| --- | --- |
| V3 scrypt | 2¹⁷ ≤ N ≤ 2²², N a power of two; 8 ≤ r ≤ 64; 1 ≤ p ≤ 16; N·r·128 ≤ 1 GiB |
| V3 PBKDF2 | 600,000 ≤ c ≤ 10,000,000 |
| Native Argon2id | 3 ≤ time ≤ 100; 64 MiB ≤ memory ≤ 2 GiB; 4 ≤ threads ≤ 64 |

`internal/keystore/kdf.go` and `native.go` define these bounds. Weak parameters and excessive
resource demands are refused. The native format checks salt, GCM nonce and ciphertext lengths
before decryption and verifies the decrypted key against the declared address. Both decryption
paths recover panics as errors, including malformed-input failures not caught by earlier checks.

Create a new key with `signer keystore create --out <path>`, or encrypt an existing hex key with
`signer keystore import --privkey-file <path> --out <path>`. The source key file must be accessible
only to its owner. Both commands write the native format and prompt twice for a new password
unless `--password-file` supplies it. Creation and import enforce `keystore.ValidatePassword`:
at least 12 bytes, at least six distinct Unicode characters, and no known weak value from the
blocklist. This strength check does not apply when unlocking an existing keystore.

`createKeystoreFileExclusive` opens a new destination with `os.O_CREATE|os.O_EXCL`, refusing
existing entries, including dangling symlinks. It checks for a regular file, sets `0600`,
syncs and closes it. There is no export subcommand; encrypted key files can still be copied.

The default password prompt uses `golang.org/x/term.ReadPassword` on a terminal. With piped
stdin it reads lines through one shared `bufio.Reader`, preserving input for subsequent prompts.
`--password-file` supports automation and requires owner-only access. Deprecated `--password`
takes precedence and warns on every use because arguments appear in process listings and shell
history. File and prompt byte buffers are zeroed after conversion to a string; the string and
argument copies cannot be cleared the same way. Passwords and private keys are not included in
logs or errors. V3 decryption reports the fixed error "could not decrypt key with given password".

`signDigest` checks persisted throttling before reading a password. Three consecutive failures
are free; the fourth starts a five-second lockout, doubling on further failures up to one hour.
`<keystore>.throttle.json` stores the state; successful unlock clears it. There is no bypass
flag, but this local file is mutable and its updates are not synced like audit appends.

The audit log in `internal/keystore/auditlog.go` records successful unlocks, failed unlocks
and signatures with their address, signed hash or failure reason:

- Verify the full chain before unlocking with `VerifyAuditLogAnchored` and before each append
  with `AppendAuditEvent`/`AppendAuditEventTo`. A broken chain blocks signing.
- Append JSON lines to `<keystore>.auditlog.jsonl` under an exclusive file lock covering
  read, verify and append. The lock uses neither flock nor cgo and is held only for that append.
  A lock file older than two minutes is treated as abandoned and removed. Main-log and anchor
  appends call `fsync` before returning.
- Record each new entry's sequence and hash in `<keystore>.auditlog.anchor.jsonl`, or at
  `--audit-anchor <path>`. An intact anchor detects deletion of recent main-log entries. A missing
  or empty anchor supplies no watermark. The default shares the keystore's storage; protection
  against rewriting both files requires an independently protected anchor, not merely another
  path or filesystem.
- In normal mode, failure to clear throttling, record a successful unlock or record the signature
  aborts output. `--unsafe-test-mode` permits these failures with warnings. Failed-unlock
  bookkeeping is best effort in either mode: errors recording the failure or its throttle state
  warn, so the log is not a guaranteed record of every attempted unlock.

`internal/hardware` defines `Signer` (`Address`, `SignDigest`, `Close`) and
`Open(DeviceKind) (Signer, error)` for `ledger`, `trezor`, `yubikey` and `hsm`. All four return a
`StubAdapter` that reports `ErrNotSupported`; no device integration exists. Unknown device names
produce a separate error. Hardware-backed signing remains the recommended production setup.
The auditor guide's §5 covers operational custody and incident response.

## The `--policy` flag

`--policy <path>` supplies the deployment values an offline signer cannot query from the chain.
Keep it independent of every package under review:

```json
{
  "chainId": "31337",
  "controller": "0x...",
  "vault": "0x...",
  "auditor": "0x...",
  "projectId": "...",
  "profileDigest": "0x...",
  "maxAttestationLifetimeHours": 720
}
```

The six deployment fields are required and non-empty; `chainId` must parse as a decimal integer.
`maxAttestationLifetimeHours` is optional, defaults to 720 (30 days) when omitted or zero, and
cannot be negative. The optional `safe` block selects Safe signing. `policy.Load` rejects unknown
fields. Signing checks project IDs exactly, addresses and digests without case sensitivity, and
the numeric typed-data chain ID against the policy string. Use the canonical decimal chain ID
and verified deployment values.

Policy mismatches fail before confirmation. `Policy.MaxAttestationLifetime` bounds the deadline
relative to both the signing time and metadata creation time, as in step 7. `--yes` requires
`--unsafe-test-mode` and skips only confirmation, leaving policy checks active. See the auditor
guide's §3 for provisioning instructions.

## Safe multisig auditor

For a Safe auditor, set `auditor` to the Safe address and add this policy fragment:

```json
"safe": { "owners": ["0x...", "0x...", "0x..."], "threshold": 2 }
```

`policy.Load` requires a valid auditor address, a non-empty list of valid, non-zero, distinct
owner addresses, and a threshold from 1 through the number of owners. It does not query on-chain
membership. Obtain the current owners and threshold independently and update policies after
changes. The Safe checks its current membership and threshold when verifying the final result;
a stale policy does not necessarily make a signature fail.

With this block, `signer sign` runs the same package checks and review, then:

- Signs `attestation.SafeMessageHash(chainId, safe, digest)`, which wraps the EIP-712 digest in
  a `SafeMessage(bytes message)` hash under the Safe's own chain/address domain. This matches
  the digest-based ERC-1271 path in Safe v1.3.0+ with `CompatibilityFallbackHandler`.
  See the [v1.3.0 handler](https://github.com/safe-global/safe-smart-account/blob/v1.3.0/contracts/handler/CompatibilityFallbackHandler.sol).
  The audit entry records the Safe message hash.
- Checks after signing that the key belongs to the policy's owners.
- Writes a `SignatureShare` to `<package-dir>/signature-share-<owner>.json`, with a lowercase
  owner address, or the `--out` path. Its nine fields are `formatVersion`, `safe`, `chainId`,
  `owner`, `primaryType`, `typedDataDigest`, `safeMessageHash`, `signature` and `signedAt`.
  `shared/schemas/safe-signature-share.schema.json` defines the format, including one 65-byte
  signature. No final result is written by this command in Safe mode.

```
signer combine --policy <path-to-policy.json> [--out <path>] <share.json>...
```

`combine` needs a policy with a `safe` block, at least one share and no key. It rejects unknown
share fields, checks field formats, and requires the policy's Safe and chain. All shares must
have the same primary type and attestation digest. It recomputes each Safe message hash, recovers
the signing address, and requires that address to match both the share's `owner` and the policy's
owner list. Duplicate owners and fewer than `threshold` distinct owners are rejected.

The result uses the Safe as `auditor`, retains the underlying attestation's EIP-712 digest, and
concatenates all supplied signatures in ascending owner-address order with
`attestation.CombineSafeSignatures`. Safe's raw owner signatures use `V` 27/28; see
[v1.4.1 signature verification](https://github.com/safe-global/safe-smart-account/blob/v1.4.1/contracts/Safe.sol).
`signed-result.json` defaults to the first share's directory; `--out` changes it. Its schema
allows multiple 65-byte chunks, while an EOA result contains one. Both result formats use
`formatVersion` `1.0` and UTC RFC 3339 `signedAt` timestamps.

Combining verifies shares, not their original package. It does not recheck controller, Vault,
project, profile, amount, nonce or expiry against the package; retain the association with what
every owner reviewed. On-chain attestation and current Safe authorization checks still apply.

`TestVectors_SafeAuditor` reproduces the fixture's attestation digest, Safe domain and message
hash, exact owner signatures and combined bytes. The fixture's `_comment` records an Anvil run
against Safe v1.4.1. The Go test and `contracts/test/unit/SafeAuditorVectors.t.sol` verify the
vector arithmetic and signatures; they do not deploy a Safe. `cmd/signer/safe_e2e_test.go` drives
the built CLI through two-owner signing, combining and rejection cases without RPC access.

## Building and testing

```sh
cd signer
go build ./...
go vet ./...
go test ./...
```

From the repository root, `make signer-test` runs `go test -race ./...`; `make signer` builds
`signer/bin/signer`. `make vectors-check` runs
`go test ./internal/attestation/... -run Vectors` alongside the contracts, server and web checks
against the shared golden vectors.

The module is `github.com/rwa-platform/signer`, using Go 1.25. Its direct dependencies are
`github.com/ethereum/go-ethereum` for crypto, keystores and common types, `golang.org/x/term` for
hidden password input, and `golang.org/x/crypto` for native Argon2id, plus transitive requirements.
`x/crypto` is also in go-ethereum's dependency graph. The local JCS and schema implementations
keep their behavior tied to the platform's canonicalization rules and restricted schema dialect.

Tests cover package size and decompression limits, traversal, symlinks, duplicate entries,
confirmation, hashes and exact signatures. `cmd/signer/e2e_test.go` builds the real binary and
signs a generated, consistent package; its rejection cases include a tampered typed-data amount.
