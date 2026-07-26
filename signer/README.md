# signer — air-gapped EIP-712 auditor signer

`signer` is a Go CLI that lets an auditor review a `.rwa` audit package on an offline
machine and produce a `signed-result.json` authorizing a specific mint or burn on
`SupplyController`. It makes **zero network requests** at any point during signing — there
is no `net/http` import anywhere in this module, by design, not merely by convention.

The shared contracts this module builds against live under `shared/`:
`shared/eip712/types.md` (the EIP-712 type strings), `shared/schemas/*.json` (the JSON
Schemas for the profile/metadata/manifest/signed-result documents), and
`shared/vectors/*.json` (the golden test vectors). See `docs/auditor/auditor-guide.md` for
the auditor-facing operational guide (machine hygiene, what to verify by hand, key
management, red flags) — this README is the engineering reference for the tool itself.

## What it does, end to end

```
signer sign <package.rwa> --keystore <path> --policy <path-to-policy.json>
```

`--policy` is required: a small local JSON file (`chainId`, `controller`,
`vault`, `auditor`, `projectId`, `profileDigest`, optional `maxAttestationLifetimeHours`)
the auditor maintains independently of any package under review. See "The `--policy` flag"
below.

1. **Read the package file** (`rwapkg.ReadPackageFile`) with its compressed size checked
   against `Stat` and re-enforced with a bounded reader before any bytes are allocated for
   the whole file, then **extract** the `.rwa` ZIP entirely in memory
   (`internal/package`, Go package name `rwapkg`). No file is ever written to disk during
   extraction. Rejects: path traversal (`../`, absolute paths, backslashes), symlink entries
   (checked two ways — decoded `fs.FileMode` and the raw Unix external-attributes bits, to
   catch archives from either convention), duplicate entry names, unsupported compression
   methods (only Store and Deflate), too many entries, and — critically — a **real**
   decompression bomb: the actual guard is a running budget of bytes *read* via
   `io.LimitReader` across the whole archive, not the (attacker-controlled) size the ZIP
   header claims. Tested against a genuine 50 MiB-inflating deflate stream, not just a
   header that lies.
2. **Parse and verify `manifest.json`** against `shared/schemas/rwa-manifest.schema.json`
   (`rwapkg.ParseManifest` — also rejects a duplicate `files[].path`, requires the three
   mandatory documents to each appear exactly once, and requires every other entry to live
   under `proofs/`), then verify the package's actual contents are *exactly* the declared
   set: every listed file present with the exact declared SHA-256 and size, and no
   extracted file left undeclared (`Manifest.VerifyFiles`).
3. **Validate `profile.json`** (`internal/profile`) against the fixed platform envelope
   (`shared/schemas/asset-profile.schema.json`: `profileVersion` const, UUID `projectId`,
   string-length bounds, `displayFields` shape, strict/no-unknown-field decoding throughout
   including nested `displayFields[]` objects), then compile the operator-defined
   `assetSchema` through the restricted JSON Schema engine (`internal/jsonschema`) against
   the pinned dialect (`docs/spec/profile-schema-dialect.md`, dialect id
   `rwa-profile-assetSchema/1`): `type`, `properties`, `required`, `additionalProperties`,
   `items`, `enum`, `const`, string length/pattern, integer/number bounds, array
   length/uniqueness, and local (`#/...`) `$ref` resolution (with cycle/depth-bounded
   resolution and no sibling keywords next to `$ref`). Every OTHER keyword is rejected by
   allowlist, not merely a blacklist — including keywords the server's full Draft 2020-12
   compiler *would* enforce (`multipleOf`, `format`, `patternProperties`, ...), so the two
   validators can't silently diverge on an unsupported keyword. Every
   `$defs` member is compiled and dialect-checked even if never reached through `$ref`, and
   `type` must be a non-empty array of unique type strings if given as an array. All
   document/instance parsing shares one set of structural limits — 1 MiB max size, depth 32,
   string length 32768, array items 4096, object properties 4096 — pinned identically on the
   server side (`shared/schemas/structural-limits.json`) and asserted equal to it in a test.
4. **Validate `metadata.json`** (`internal/metadata`) against
   `shared/schemas/metadata.schema.json` (`recordId` pattern
   `^[A-Za-z0-9._:-]{1,128}$`, `issuance.amount` as a decimal-string pattern, `proofs[].sha256`
   as lowercase hex, `createdAt` as RFC 3339, strict decoding of nested `issuance`/`proofs[]`
   objects), then validate its `asset` object against the profile's compiled `assetSchema`
   (`Profile.ValidateAsset`), then cross-check that `metadata.projectId`/`issuance.unit`
   match `profile.projectId`/`profile.tokenUnit` exactly — a package cannot pair an audited
   profile with metadata that describes a different project or legal unit.
5. **Recompute everything independently** rather than trust what the package claims:
   - RFC 8785 JSON Canonicalization (`internal/canonical`) of both `profile.json` and
     `metadata.json` — a hand-written strict JSON parser (rejects duplicate object keys,
     validates UTF-8, enforces configurable depth/string/array/object-key/byte limits) plus
     a canonical serializer (object keys sorted by **UTF-16 code unit**, per RFC 8785 —
     this is not the same as sorting by Unicode code point for supplementary-plane
     characters, and is tested against that exact surrogate-pair-vs-BMP quirk; numbers
     formatted via the ECMAScript `Number::toString` algorithm; minimal string escaping).
   - `profileDigest`/`metadataDigest` = **SHA-256** (not keccak) of the canonical bytes
     (`internal/cid` also derives the IPFS CIDv1 — codec `raw` 0x55, multihash sha2-256 —
     from the same canonical bytes and digest, verified against real `ipfs add
     --cid-version=1 --raw-leaves --hash=sha2-256 --only-hash` output).
   - `recordKey = keccak256(bytes(recordId))` for a mint (`attestation.RecordKey`).
   - The full EIP-712 digest (`internal/attestation` — see below).
6. **Cross-check `typed-data.json`** (`shared/schemas/typed-data.schema.json`) against all
   of the above: `profileDigest`/`metadataDigest` match the recomputed values;
   `metadata.issuance.amount` equals the typed `amount` exactly (compared as `big.Int`, not
   string equality, so leading-zero decimal forms still match correctly); the mint
   `recordId`/`recordKey` pair is internally consistent; `primaryType` matches
   `manifest.json`'s.
7. **Check the independently-provisioned `--policy` file** (`internal/policy`):
   chain ID, controller, vault, auditor, project ID, and profile digest must all match the
   package exactly — a mismatch on any of these is a hard failure, never merely a warning,
   since (unlike step 6) the policy file's values never come from the package under review.
   Also enforces `now < validUntil <= min(now, metadata.createdAt) + policy's max
   attestation lifetime` (30 days by default) — a zero, already-expired, or far-future
   `validUntil` is rejected here, before any signature is produced.
8. **Render a review screen** (`internal/ui`) of every security-critical typed field
   (chain ID, controller, vault, auditor, both digests, amount, nonce, `validUntil`
   alongside the policy window it was checked against, `recordKey`/`operationId`, the
   computed digest itself, the profile/metadata project IDs, unit/decimals/normalized
   amount, and the policy file path) plus the profile's `displayFields` resolved by JSON
   pointer against the metadata's `asset` object (`displayFields` is documentation for the
   human reviewer only — it never affects hashing or validation, and the implementation
   reflects that: it's read only after all validation/digest computation is done). Any
   `metadata.proofs[]` entry with no attached
   file matching its declared hash is flagged with an explicit warning line rather than
   silently passed over.
9. **Require explicit confirmation** (`ui.Confirm`, reads stdin): only a literal `y` or
   `yes` (case-insensitive, whitespace-trimmed) proceeds. Anything else — including a blank
   line or EOF — is "no." Fails closed. `--yes` skips this step but requires
   `--unsafe-test-mode` alongside it — scripted/CI use only; `--policy` is still fully
   enforced regardless.
10. **Sign** the raw 32-byte EIP-712 digest (no EIP-191 prefix) via `crypto.Sign`
    (go-ethereum, deterministic per RFC 6979), producing a standard 65-byte
    `[R‖S‖V]` signature with `V` normalized to `{27,28}`. Before writing anything, the
    signer independently recovers the signer address from its own output and refuses to
    proceed if it doesn't match the declared `auditor` — it will not write a signature that
    would fail on-chain recovery.
11. **Write `signed-result.json`** (`internal/output`), validated against
    `shared/schemas/signed-result.schema.json` before it's written, defaulting to
    `<package-dir>/signed-result.json`.

Any failure at any step exits non-zero with a specific error and writes nothing. There is
no partial output.

## Module layout

```
signer/
├── cmd/signer/           CLI entry point (package main → binary "signer")
│   ├── main.go             flag parsing + the pipeline above
│   ├── keystore_cmd.go     "signer keystore create"/"import" subcommands
│   ├── typeddata.go        typed-data.json parsing/validation
│   └── review.go           JSON-pointer resolution + review-screen field building
└── internal/
    ├── canonical/         RFC 8785 JCS: parser + canonical serializer
    ├── cid/                CIDv1 (raw, sha2-256) from canonical bytes
    ├── jsonschema/         operator assetSchema dialect validator (docs/spec/profile-schema-dialect.md)
    ├── profile/            Asset Profile envelope validation + assetSchema compilation
    ├── metadata/           Metadata envelope validation
    ├── package/             .rwa ZIP extraction + manifest verification (Go pkg name: rwapkg)
    ├── policy/              --policy file loading: the auditor's independent trust root
    ├── attestation/        EIP-712 domain/typehash/digest, Sign, Recover
    ├── keystore/            keystore load/create + unlock hardening -- see "Keys and secrets"
    ├── hardware/            Signer interface, DeviceKind/Open factory, non-production StubAdapter
    │                        (design: docs/adr/ADR-006-hardware-wallet-signing.md)
    ├── output/              signed-result.json construction + schema validation
    └── ui/                  review-screen rendering, mint/burn banner, fail-closed confirm prompt
```

`internal/package`'s Go package identifier is `rwapkg`, not `package` — `package` is a
reserved word in Go, so the directory name and the package clause necessarily differ; every
importer refers to it as `rwapkg`.

## EIP-712 construction and vector reproduction

`internal/attestation` builds the domain separator and both attestation type hashes
directly from the exact canonical strings in `shared/eip712/types.md`:

```
EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)
MintAttestation(address auditor,bytes32 profileDigest,bytes32 recordKey,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)
BurnAttestation(address auditor,bytes32 profileDigest,bytes32 operationId,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)
```

Since every field of both attestation structs is an EVM value type (`address`, `bytesN`,
`uintN`), `abi.encode(...)` for `hashStruct` is exactly the concatenation of each field's
32-byte left-padded word — implemented directly (`common.LeftPadBytes` + `crypto.Keccak256Hash`)
rather than through go-ethereum's general-purpose ABI package, since there's no dynamic
type here to justify that dependency. `uint64 validUntil` is encoded as a full 32-byte word
per the spec note in `types.md`.

`internal/attestation/vectors_test.go`'s `TestVectors` loads `shared/vectors/typehashes.json`
and `shared/vectors/mint-eip712.json` at test time and asserts, independently recomputed
from this package's own code: all five frozen type hashes, the domain separator, the
`hashStruct` value, the final digest, and — signing the vector's digest with the vector's
`signerPrivateKey` — the **exact golden signature byte string**, not just that it recovers
to the right address. This works because go-ethereum's `crypto.Sign` uses RFC 6979
deterministic `k` generation, so the same digest and key always produce the same signature.
`internal/canonical/jcscid_vector_test.go` does the equivalent for JCS/CID against
`shared/vectors/jcs-cid.json` — canonical bytes, SHA-256, and the CIDv1 string, all
byte-for-byte. Together these two tests are the module's proof of cross-language parity
with the Solidity contracts and the Go server, which construct and verify the same values
independently.

## Keys and secrets

- `internal/keystore.Load` decrypts a keystore file and returns the private key and address;
  `internal/keystore.Zero` clears the key's `D` scalar to zero once signing is done — call it via
  `defer` immediately after loading, as `cmd/signer/main.go` does. `Load` accepts either format,
  auto-detected from the file's own `"format"` field:
  - a standard **Ethereum V3 JSON keystore** (`keystore.DecryptKey`), for compatibility with
    externally-generated keystores; or
  - this signer's own **`rwa-argon2id-v1`** format (`internal/keystore/native.go`): Argon2id key
    derivation + AES-256-GCM, generated by `signer keystore create`/`import`.

  Either way, `Load` first bounds the file's own size (`MaxKeystoreFileBytes`, 1 MiB, checked
  before `os.ReadFile` allocates anything) and rejects a group/world-readable keystore file
  (`checkKeystoreFilePermissions`, mirroring the `--password-file` check below), then enforces a
  **minimum AND maximum KDF-strength policy on every load, not just at creation**: scrypt
  2¹⁷≤N≤2²²/r≤64/p≤16 (plus an
  absolute ~1 GiB cap on N·r's memory footprint and a power-of-two check on N) or
  600,000≤PBKDF2-c≤10,000,000 for V3 (`internal/keystore/kdf.go`), 3≤Argon2id-time≤100 /
  64 MiB≤memory≤2 GiB / 4≤threads≤64 for the native format (`internal/keystore/native.go`) — a file
  whose declared parameters are either downgraded (weak, brute-forceable) or driven to an extreme
  (a memory/CPU exhaustion attempt via a corrupted or attacker-supplied file) is refused before any
  KDF work is attempted, on the same "checked before expensive work, not just before weak work"
  principle in both directions. The native format additionally validates its salt, AES-GCM nonce,
  and ciphertext at their exact required byte lengths before any decryption call: Go's
  `crypto/cipher` GCM implementation *panics* — not merely errors — on a wrong-length nonce, so
  this check is what stands between a malformed keystore file and a crashed process, not just
  input hygiene. `Load`'s V3 and native decryption paths are additionally wrapped in a `recover()`
  as a last-resort net, converting any panic neither of the above checks anticipated into an
  ordinary error.
- **`signer keystore create --out <path>`** generates a new key; **`signer keystore import
  --privkey-file <path> --out <path>`** encrypts an existing key read from an owner-only-readable
  hex file (never a CLI argument). Both always write the native Argon2id format and enforce
  `keystore.ValidatePassword` (12-character minimum, rejects a short blocklist of known-weak
  values and low-entropy repeats — `internal/keystore/policy.go`) on the new password, prompting
  for it twice to catch a typo. Both write the output file via `createKeystoreFileExclusive`
  (`cmd/signer/keystore_cmd.go`): `os.O_CREATE|os.O_EXCL` so the create-then-check-then-write race
  a plain `os.Stat` followed by `os.WriteFile` had is closed atomically at the OS
  level — this also refuses to write through a symlink at `--out`, dangling or not, since `O_EXCL`
  fails on any existing directory entry, symlink included, without following it — then verifies
  the result is a regular file, re-asserts mode `0600` (the mode passed to `OpenFile` is subject to
  umask), and `fsync`s it before closing. There is no export subcommand: the only way key material
  leaves a keystore file is as a signature over a digest, via `signer sign`.
- **Failed-unlock throttling** (`internal/keystore/throttle.go`): since `signer` is a stateless
  CLI invoked fresh per attempt, unlock-failure state is persisted next to the keystore file
  (`<path>.throttle.json`). The first few failures are free; further ones trigger an exponentially
  growing lockout (capped at one hour), checked in `cmd/signer/main.go`'s `signDigest` *before* a
  password is even read, and cleared on a successful unlock.
- **Tamper-evident, durably-appended, lock-serialized audit log** (`internal/keystore/auditlog.go`):
  every unlock attempt (success/failure) and successful signature is appended as a hash-chained
  JSON line to `<keystore-path>.auditlog.jsonl` — only non-sensitive metadata (address, digest,
  failure reason), never a password or key. Three properties beyond the hash chain itself:
  - **Verify-before-append and verify-before-sign.** `AppendAuditEvent` re-verifies the *entire*
    existing chain (not just trusts the last line) before adding to it, and refuses to extend a
    broken one. `cmd/signer/main.go`'s `signDigest` calls `VerifyAuditLogAnchored` (see below)
    before doing anything else against a keystore — a corrupted or truncated audit trail blocks
    unlock/sign entirely, rather than accumulating a fresh-looking entry on top of a compromised
    history.
  - **Locked, durable appends.** `acquireAuditLock` takes an exclusive, portable (no flock/cgo)
    file lock spanning the whole read-verify-append sequence, so two `signer sign` processes
    racing against the same keystore cannot fork the sequence/hash chain; a lock abandoned by a
    crashed process is broken automatically after two minutes. Every append (main log and anchor)
    is `fsync`ed before the function returns.
  - **Independent anchor log**, `DefaultAnchorPath` (`<keystore>.auditlog.anchor.jsonl` by
    default, overridable via `signer sign --audit-anchor <path>`): each append also records the
    new entry's (seq, hash) there. `VerifyAuditLogAnchored` cross-checks the main log's tail
    against this watermark, catching wholesale truncation of the newest main-log entries — which
    an internal hash chain alone cannot, since a truncated-but-otherwise-untouched log is still a
    perfectly valid (shorter) chain on its own. The default same-disk anchor location only
    resists a careless/partial truncation; real protection requires `--audit-anchor` pointing at
    genuinely separate media (see `docs/auditor/auditor-guide.md` §5, which documents this
    limitation to the auditor directly).
  - **Fail-closed writes outside `--unsafe-test-mode`.** Previously, a failure to record the
    unlock-success, throttle-clear, or signed-event entries only printed a warning and continued
    -- meaning a real unlock or signature could occur with no durable record of it. `signDigest`
    now aborts (no `signed-result.json` is ever written) if any of those three writes fails,
    unless `--unsafe-test-mode` is set (the same flag already required for any non-interactive
    signing), in which case it warns and continues as before, for scripted/CI use.
- **Passwords and private keys are never logged or wrapped into an error string anywhere in
  this module.** go-ethereum's own `DecryptKey` error text does not include the password
  either, so error messages are safe to print as-is.
- The interactive stdin prompt is the default and hides input (no terminal echo) whenever
  stdin is a real terminal, via `golang.org/x/term.ReadPassword`.
  When stdin isn't a terminal (piped/scripted input), the prompt falls back to a plain line
  read from a single shared `bufio.Reader` over stdin (`readPasswordPrompt`/`newStdinLineReader`
  in `cmd/signer/main.go`) — shared across calls specifically so `signer keystore create`'s
  double password prompt reads two distinct lines rather than a fresh `bufio.Reader` per call
  over-reading and discarding the second one — since `term.ReadPassword` itself errors on a
  non-tty descriptor. `--password-file <path>` exists for automation and requires an
  owner-only-readable file (rejected if group/world-readable); `--password <value>`
  exists but is deprecated and prints a loud warning on every use, since it's visible in process
  listings and shell history. Whichever path is used, the password byte buffer is zeroed
  (`keystore.ZeroBytes`) as soon as it's been copied into the string the decryption path needs.
  See `docs/auditor/auditor-guide.md` §5 for the operational guidance this implies.
- `internal/hardware` defines a `Signer` interface (`Address`, `SignDigest`, `Close`) and an
  `Open(DeviceKind) (Signer, error)` factory for `ledger`/`trezor`/`yubikey`/`hsm`, all currently
  resolving to `StubAdapter` (`ErrNotSupported`) — no device has a working integration in this
  build, and none is attempted here since real hardware cannot be exercised in this environment.
  `--hardware <device>` on the CLI selects a device by name (previously a bare boolean); an
  unrecognized device name is a distinct error from "not yet implemented." See
  `docs/adr/ADR-006-hardware-wallet-signing.md` for the integration seam, the device shortlist and
  their EIP-712 constraints, and why hardware-backed signing — not the software keystore above —
  is the recommended production configuration.

## The `--policy` flag

There is no on-chain source of truth an air-gapped machine can reach to verify chain ID,
controller, vault, auditor address, project ID, or profile digest against. `--policy
<path>` (`internal/policy`) is **required** on every invocation and points at
a small local JSON file the auditor maintains independently of any package under review:

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

Every field but `maxAttestationLifetimeHours` (default 720h = 30 days) is required and
checked against the package with a **hard failure**, not a printed warning, on any
mismatch — there is no way to omit a check the way the old optional `--expect-*` flags
allowed. `Policy.MaxAttestationLifetime` also bounds `validUntil`: the signer enforces
`now < validUntil <= min(now, metadata.createdAt) + MaxAttestationLifetime`, rejecting a
zero, already-expired, or far-future attestation before any confirmation prompt or
signature. `--yes` (skip interactive confirmation) additionally requires
`--unsafe-test-mode`; both exist for scripted testing/CI only, and `--policy` is enforced
either way — noninteractive signing never relies on package-supplied values alone.
`docs/auditor/auditor-guide.md` §3 has the full field reference and operational guidance.

## Building and testing

```sh
cd signer
go build ./...
go vet ./...
go test ./...
```

From the repo root: `make signer-test`. `make vectors-check` runs
`go test ./internal/attestation/... -run Vectors` specifically (plus the Solidity and
server equivalents), so the cross-language golden vectors are checked together.

Module: `github.com/rwa-platform/signer`, Go 1.25. Direct third-party dependencies are
`github.com/ethereum/go-ethereum` (crypto, keystore, common/types), `golang.org/x/term`
(hidden password-prompt input only), and `golang.org/x/crypto` (`argon2` only,
for the native keystore format's KDF) plus their transitive
requirements — deliberately minimal. `golang.org/x/crypto` was already present transitively
via go-ethereum before this addition (it is not a new module in the dependency graph, only a
promotion from indirect to direct, with exactly one subpackage imported). `internal/canonical`
(JCS) and `internal/jsonschema` are
hand-written rather than pulling in a JSON-Schema or JCS library, both so there's one fewer
dependency to audit on an air-gapped tool and so their behavior can be pinned exactly to
the profile-schema and canonicalization rules this platform uses — including deliberately
narrow subsets and quirks (the UTF-16 sort order) that a general-purpose library wouldn't
necessarily match.

Every package with logic beyond a thin wrapper has its own `_test.go`; security-relevant
behavior — the ZIP-bomb defense, path traversal, symlink rejection, duplicate-entry
rejection, fail-closed confirmation, digest/signature vector reproduction, and the full CLI
pipeline against a freshly-assembled self-consistent `.rwa` package
(`cmd/signer/e2e_test.go`, which builds the real binary and runs it end to end, including a
negative test proving a tampered typed-data amount is rejected before any signature is
written) — is exercised directly, not just implied by the happy path.
