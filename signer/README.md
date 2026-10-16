# signer: air-gapped EIP-712 auditor signer

`signer` reviews `.rwa` packages offline and creates `signed-result.json` authorizing one mint
or burn on `SupplyController`. Safe owners sign individual shares; `signer combine` assembles
the result. Signing makes no network requests.

The [auditor guide](../docs/auditor/auditor-guide.md) covers machine hygiene, evidence review and
key management. This README covers validation, formats and tests against `shared/eip712/types.md`,
`shared/schemas/*.json` and `shared/vectors/*.json`.

## What it does, end to end

```
signer sign <package.rwa> --keystore <path> --policy <path-to-policy.json>
```

Every run needs an independent local `--policy` pinning `chainId`, `controller`, `vault`,
`auditor`, `projectId` and `profileDigest`; lifetime and Safe settings are optional
(see "The `--policy` flag"). The signing flow is:

1. `rwapkg.ReadPackageFile` checks compressed size with `Stat` and a limited reader. Extraction
   stays in memory and rejects empty archives, excessive counts, unsafe paths, Unix symlinks,
   duplicate names and methods other than Store/Deflate. Unsafe names include absolute paths,
   backslashes and names changed by `path.Clean`, such as `../`, `./` and trailing slashes.
   Expanded-size limits count bytes read per entry and in total, regardless of ZIP-header sizes.
2. `rwapkg.ParseManifest` checks the `shared/schemas/rwa-manifest.schema.json` envelope. Require
   `profile.json`, `metadata.json` and `typed-data.json` once each; paths are unique and extras
   live under `proofs/`.
   `VerifyFiles` requires matching sizes/SHA-256 and no undeclared payloads; the manifest excludes
   itself from its inventory.
3. `internal/profile` checks fixed `profileVersion`, UUID `projectId`, bounded strings,
   `displayFields` and unknown fields, including nested display-field members. Optional `legal`
   follows the [Legal descriptor rules](../docs/spec/asset-profile.md#legal-descriptor).
   `internal/jsonschema` compiles `assetSchema` under `rwa-profile-assetSchema/1`
   (`docs/spec/profile-schema-dialect.md`), accepting:

   - Validation: `type`, `properties`, `required`, `additionalProperties`, `items`, `$ref`, `enum`,
     `const`, `minLength`, `maxLength`, `pattern`, `minimum`, `maximum`, `exclusiveMinimum`,
     `exclusiveMaximum`, `minItems`, `maxItems`, `uniqueItems`.
   - Annotations/definitions: `$schema`, `$id`, `$comment`, `$defs`, `title`, `description`,
     `default`, `examples`, `deprecated`, `readOnly`, `writeOnly`.

   `type` accepts any JSON type or a nonempty array of unique types. Local `$ref` (`#` or `#/...`)
   permits no siblings; cycles, depth and processed nodes are bounded. Unsupported keywords,
   including `multipleOf`, `format` and `patternProperties`, fail in every compiled schema node,
   including unused `$defs`; enum/const/default/examples values remain opaque data.
   Profiles, metadata and raw schema instances use defaults of 1 MiB, depth 32, 32768 UTF-8 bytes
   per string/key, 4096 array items and 4096 properties. `TestDialectStructuralLimitsMatchSharedConstants`
   compares defaults with the limits copied into `shared/vectors/schema-dialect-conformance.json`
   from `shared/schemas/structural-limits.json`.
4. `internal/metadata` requires `recordId` matching `^[A-Za-z0-9._:-]{1,128}$`, positive decimal
   `issuance.amount` up to 2^256-1, lowercase proof hashes and RFC 3339 `createdAt`. Unknown fields,
   including in `issuance` and proofs, fail. `Profile.ValidateAsset` checks `asset`; metadata,
   profile and policy project IDs must match, and `issuance.unit` must equal `profile.tokenUnit`.
5. Profile/metadata validation canonicalizes through `internal/canonical`, then computes SHA-256.
   The CLI requires the profile digest to match policy/manifest and metadata to match the manifest.
   Canonical parsing rejects duplicate keys/invalid UTF-8 and applies structural limits;
   serialization uses UTF-16 key order, ECMAScript `Number::toString` and minimal escaping.
   `internal/cid` derives raw-codec (0x55), sha2-256 CIDv1 from those bytes for vectors; signing
   does not call it. CID fixtures use `ipfs add --cid-version=1 --raw-leaves --hash=sha2-256 --only-hash`.
6. `cmd/signer/typeddata.go` checks `shared/schemas/typed-data.schema.json` field rules and package
   digests/`primaryType`. Amounts compare as `big.Int`, accepting equivalent leading-zero forms.
   Mint data must match `recordId` and `recordKey = keccak256(bytes(recordId))`.
7. Check policy chain, controller, Vault and auditor; require
   `now < validUntil <= min(now, metadata.createdAt) + maxAttestationLifetime` (default 30 days).
   Reject mismatches/deadlines before review, then compute the EIP-712 digest in `internal/attestation`.
8. The CLI builds fields for `internal/ui`: mint/burn banner, primary type, both project IDs,
   unit/decimals, chain/controller/auditor/Vault, profile/metadata/EIP-712 digests, raw/normalized
   amount, nonce, deadline/window and policy path.
   Mint adds `recordId`/`recordKey`; burn adds `operationId`; Safe mode adds its type, owners,
   threshold and message hash. Resolve RFC 6901 `displayFields` pointers against metadata `asset`,
   warning with "(not present)" when unresolved. Resolution follows hashing; definitions are
   validated and profile-digest-bound. Legal terms precede asset fields; document references
   remain unfetched text. Proofs show matching attachments or `NO ATTACHED FILE MATCHES THIS HASH`.
   `textsafe.SanitizeForTerminal` escapes controls, ANSI and bidi characters in titles/labels/values.
   Parsing also rejects non-printable `assetType`, `tokenUnit`, `recordIdLabel`, display labels,
   `issuance.unit` and proof types.
9. `ui.Confirm` accepts only trimmed, case-insensitive `y`/`yes`. Blank, other answers and EOF
   without input decline. Test/CI flags `--yes --unsafe-test-mode` skip confirmation, preserving
   policy checks.
10. Unlock and sign with deterministic RFC 6979 `crypto.Sign`. EOA signing uses the raw EIP-712
    digest and 65-byte `[R‖S‖V]` with V=27/28. Auditor-address matching happens afterward: mismatch
    prevents output but may leave unlock/signature audit entries.
11. Write `signed-result.json` beside the package or at `--out`, after `SignedResult.Validate`
    checks field formats.

Safe mode replaces steps 10/11 as described below. Errors return nonzero status; validation,
confirmation and identity failures precede output. Write errors may leave partial files;
audit, anchor and throttle side effects follow "Keys and secrets".

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

Import `internal/package` as `rwapkg`; `package` is reserved in Go.

## EIP-712 construction and vector reproduction

`internal/attestation` uses the type strings in `shared/eip712/types.md`:

```
EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)
MintAttestation(address auditor,bytes32 profileDigest,bytes32 recordKey,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)
BurnAttestation(address auditor,bytes32 profileDigest,bytes32 operationId,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)
```

Attestations encode static 32-byte words: addresses/uints are left-padded, bytes32 values are
already word-sized, and `validUntil` fills a word despite its `uint64` type. Encoding uses
`common.LeftPadBytes` and `crypto.Keccak256Hash` without a general ABI encoder. Profile/metadata
digests use SHA-256; EIP-712 and record-key hashes use keccak256.

`TestVectors` (`internal/attestation/vectors_test.go`) reproduces type hashes from
`shared/vectors/typehashes.json`, then the domain separator, struct hash, digest and exact
signature from `shared/vectors/mint-eip712.json` using its `signerPrivateKey`. Safe vectors are
covered below. `internal/canonical/jcscid_vector_test.go` reproduces canonical bytes, SHA-256
and CIDv1 from `shared/vectors/jcs-cid.json`; serializer tests also cover supplementary-character
UTF-16 ordering.

## Keys and secrets

`internal/keystore.Load` detects Ethereum V3 (`keystore.DecryptKey`) or native `rwa-argon2id-v1`
(Argon2id/AES-256-GCM), returning key/address. It rejects group/other permission bits and
`Stat` sizes above `MaxKeystoreFileBytes` (1 MiB), then rechecks length after reading.
The post-read check rejects growth but does not cap `os.ReadFile` allocation.

Both formats bound KDF work before derivation:

| KDF             | Accepted parameters                                                      |
| --------------- | ------------------------------------------------------------------------ |
| V3 scrypt       | 2¹⁷ ≤ N ≤ 2²², N a power of two; 8 ≤ r ≤ 64; 1 ≤ p ≤ 16; N·r·128 ≤ 1 GiB |
| V3 PBKDF2       | 600,000 ≤ c ≤ 10,000,000                                                 |
| Native Argon2id | 3 ≤ time ≤ 100; 64 MiB ≤ memory ≤ 2 GiB; 4 ≤ threads ≤ 64                |

`kdf.go`/`native.go` define these bounds. Native loading checks salt, GCM nonce and ciphertext
lengths before derivation, then verifies the decrypted address. Both decryption paths recover
KDF/cipher panics as errors.

`signer keystore create --out <path>` creates a key;
`signer keystore import --privkey-file <path> --out <path>` imports owner-only hex key files. Both write native format, prompt twice unless
`--password-file` is supplied, and enforce `keystore.ValidatePassword`: >=12 bytes, six distinct
Unicode characters and no case-insensitive blocklist match. Existing-password unlocks have no
strength check. `createKeystoreFileExclusive` uses `O_CREATE|O_EXCL`, rejects existing entries
including dangling symlinks, checks regular-file type, sets 0600, syncs and closes. No export
subcommand exists; encrypted files can still be copied.

Terminal prompts use `golang.org/x/term.ReadPassword`; piped input uses a shared `bufio.Reader`
to preserve later lines. Owner-only `--password-file` supports automation. Deprecated `--password`
takes precedence and warns about process-list/shell-history exposure. File/prompt buffers are
zeroed after string conversion; string/argument copies remain. Defer `keystore.Zero` after Load
as the CLI does: it clears scalar backing words, not every runtime/library copy. Keep secrets
out of audit metadata; V3 password failure reports "could not decrypt key with given password".

Before reading a password, `signDigest` checks `<keystore>.throttle.json`. Three consecutive
failures are free; the fourth starts five seconds, doubling to one hour. Success clears state.
There is no bypass flag, but deleting it or making it unreadable resets recorded failures;
malformed JSON creates a fresh short lockout on each read. Throttle writes are not synced.

`internal/keystore/auditlog.go` records unlock successes/failures and signatures with address,
signed hash or failure reason:

- `VerifyAuditLogAnchored` checks chain/watermark before unlock; `AppendAuditEvent`/`AppendAuditEventTo`
  recheck the main chain before appending. Broken chains block signing.
- `<keystore>.auditlog.jsonl` uses an exclusive file lock for each read/verify/append, without
  flock/cgo. Age above two minutes triggers assumed-abandoned takeover, without a liveness check.
- Each synced main entry's sequence/hash is appended to `<keystore>.auditlog.anchor.jsonl` or
  `--audit-anchor`. Success syncs both files; anchor failure can leave the main entry persisted.
  An intact watermark detects tail deletion/recomputed history. Missing/empty anchors supply
  none; use independent protection, not merely a different writable path/filesystem.
- Normal mode aborts output on throttle-clear, unlock-success or signature-audit failure;
  `--unsafe-test-mode` warns instead. Failed-unlock bookkeeping is best effort in both modes,
  so not every unlock attempt is guaranteed a record.

`internal/hardware` exposes `Signer` (`Address`, `SignDigest`, `Close`) and `Open(DeviceKind)`
for `ledger`, `trezor`, `yubikey`, `hsm`. Recognized names return `StubAdapter`: Address/SignDigest
report `ErrNotSupported`, Close succeeds; unknown names error separately. Device integration
is required before hardware signing can be used. The auditor guide §5 covers recommended
hardware custody and incident response.

## The `--policy` flag

Maintain `--policy <path>` independently of packages; it supplies deployment values the offline
signer cannot query:

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

All six deployment fields are required/nonempty; `chainId` must parse as a decimal integer.
`policy.Load` rejects unknown fields. `maxAttestationLifetimeHours` defaults to 720 (30 days)
when omitted/zero and rejects negatives; optional `safe` selects multisig signing.

Signing compares project IDs exactly, addresses/digests case-insensitively, and the numeric
typed-data chain ID with the policy string. Use canonical decimal chain IDs and independently
verified values. Policy/time mismatches fail before confirmation under step 7's lifetime bound.
`--yes` requires `--unsafe-test-mode` and skips only confirmation. See auditor guide §3 for provisioning.

## Safe multisig auditor

Set `auditor` to the Safe address and add:

```json
"safe": { "owners": ["0x...", "0x...", "0x..."], "threshold": 2 }
```

`policy.Load` checks the auditor address, nonempty valid/nonzero/distinct owners and threshold
1..owner count. It queries no chain state: provision owners/threshold independently and update
after changes. Safe verifies current membership/threshold; stale policy does not necessarily
invalidate signatures.

`signer sign` performs the same validation/review, then signs
`attestation.SafeMessageHash(chainId, safe, digest)` under the Safe's chain/address domain,
wrapping the attestation as `SafeMessage(bytes message)`. This follows the digest-based
ERC-1271 `CompatibilityFallbackHandler` path ([v1.3.0 handler](https://github.com/safe-global/safe-smart-account/blob/v1.3.0/contracts/handler/CompatibilityFallbackHandler.sol)).
The audit records this Safe hash. After signing, the key must match a policy owner.

Safe mode writes a `SignatureShare` at `<package-dir>/signature-share-<owner>.json`, using a
lowercase owner in the filename, or at `--out`, rather than a final result. Its fields are `formatVersion`, `safe`,
`chainId`, `owner`, `primaryType`, `typedDataDigest`, `safeMessageHash`, `signature`, `signedAt`;
`shared/schemas/safe-signature-share.schema.json` requires one 65-byte signature.

```
signer combine --policy <path-to-policy.json> [--out <path>] <share.json>...
```

`combine` needs Safe policy/shares, not a key. It rejects unknown fields and bad formats, requires
policy Safe/chain and common primary type/digest, recomputes Safe hashes, and requires recovery
to the declared owner with policy membership. Duplicate owners or fewer than threshold fail.

`attestation.CombineSafeSignatures` concatenates all supplied shares in ascending owner order
with raw V=27/28 ([v1.4.1 signature verification](https://github.com/safe-global/safe-smart-account/blob/v1.4.1/contracts/Safe.sol)).
The result retains the attestation digest with the Safe as `auditor`, written beside the first
share or at `--out`. Results accept multiple 65-byte chunks; EOA output has one. Both formats use
`formatVersion` `1.0` and UTC RFC 3339 `signedAt`.

Combining does not recheck the original package, controller, Vault, project, profile, amount,
nonce or expiry; retain each share's association with what owners reviewed. On-chain attestation
and current Safe authorization checks still apply.

`TestVectors_SafeAuditor` reproduces the attestation/Safe hashes, exact owner signatures and
combined bytes from `shared/vectors/safe-auditor.json`. Its `_comment` records an Anvil/Safe v1.4.1
run. Go and `contracts/test/unit/SafeAuditorVectors.t.sol` verify arithmetic/signatures without
deploying Safe; `cmd/signer/safe_e2e_test.go` tests CLI two-owner signing, combining and refusals
without RPC.

## Building and testing

```sh
cd signer
go build ./...
go vet ./...
go test ./...
```

From the repository root, `make signer-test` runs `go test -race ./...`, `make signer` builds
`signer/bin/signer`, and `make vectors-check` runs `go test ./internal/attestation/... -run Vectors`
alongside contract/server/web shared-vector checks.

`github.com/rwa-platform/signer` uses Go 1.26.9. Direct dependencies are `github.com/ethereum/go-ethereum`
(crypto/keystores/types), `golang.org/x/term` (hidden input) and `golang.org/x/crypto` (Argon2id),
plus transitive requirements; x/crypto also appears through go-ethereum. Local JCS/schema code
implements the platform's canonicalization rules and restricted dialect.

Tests cover compressed/expanded limits, traversal, symlinks, duplicates, confirmation, hashes
and exact signatures. `cmd/signer/e2e_test.go` builds the CLI, signs a generated valid package
and rejects tampering, including changed typed-data amounts.
