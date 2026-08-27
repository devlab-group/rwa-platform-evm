# Auditor guide: air-gapped signing with `signer`

Audience: the person or institution holding the **auditor** key for a deployment. This
guide explains what you are attesting to, how to run the offline `signer` CLI safely, and
what to check by hand that the tool cannot check for you.

This guide stands on its own, but the authoritative details live in `docs/spec/contracts.md`
(the `SupplyController` check order). If anything here conflicts with the contract spec, the spec
wins — tell the platform operator so this guide can be corrected.

## 1. What you are attesting to

> Every increase in token supply requires a valid auditor signature bound to this chain,
> this `SupplyController`, this project profile, one metadata record, one amount, one
> Vault, and one unexpired nonce.

Concretely, signing a `MintAttestation` authorizes `SupplyController.mint` to create
exactly `amount` tokens into the project `Vault` — permanently, the moment anyone submits
your signature on-chain. Signing a `BurnAttestation` authorizes burning `amount` tokens
from Vault inventory — also permanent, and only Vault-held tokens (never a circulating
holder's balance or funds locked in `RedemptionEscrow`).

Your signature is **not** encrypted and **not** a promise about the future: it is a
one-time cryptographic statement that, at the moment you signed, you verified the recorded
backing supports this exact supply operation. The chain enforces the mechanics; it cannot
verify that the underlying real-world asset actually exists. That verification is entirely
your job, off-chain, before you type "yes".

The contract will refuse a stale or misdirected signature (wrong chain, wrong
`SupplyController`, wrong profile, wrong Vault, wrong auditor, expired, reused nonce or
record key — see `docs/spec/contracts.md` for the exact check order), but it will **not**
refuse a signature over a record that is simply untrue. Nothing on-chain substitutes for
your review.

## 2. Machine and binary hygiene

- Sign only on a machine that **never** connects to any network. The signer makes no network
  request during signing — but an air-gapped *policy* on the machine itself is what actually
  enforces "never", not the binary's good behavior alone.
- Verify the `signer` binary's checksum/signature against the value published by the
  platform operator before every use, especially after any update. Release binaries are
  reproducible, checksummed, and code-signed where operator infrastructure permits.
- Move `.rwa` packages onto the air-gapped machine and `signed-result.json` off of it via a
  dedicated transfer medium (e.g. a USB drive reserved for this purpose). Scan or otherwise
  sanity-check that medium before each use; a `.rwa` file is just a ZIP, and the signer's
  package parser is defensive (rejects path traversal, symlinks, zip bombs, oversized
  entries — see `signer/internal/package`), but there is no reason to make it prove that
  property against a hostile file more often than necessary.
- Keep your keystore file and its password on the air-gapped machine only, in separate
  locations (e.g. keystore on disk, password in a password manager or safe, never both on
  the same USB drive you use for package transfer).

## 3. Running the signer

```
signer sign <package.rwa> --keystore <path-to-keystore.json> --policy <path-to-policy.json>
```

**`--policy` is required on every invocation.** It points at a small local JSON file you
maintain yourself, independently of anything the package supplies — never generated from
or copied out of a `.rwa` package under review:

```json
{
  "chainId": "31337",
  "controller": "<SupplyController address>",
  "vault": "<Vault address>",
  "auditor": "<your own address>",
  "projectId": "<this deployment's projectId>",
  "profileDigest": "<the currently-active profileDigest>",
  "maxAttestationLifetimeHours": 720
}
```

Write this file down once, after the project's deployment was verified (or copy it from
`docs/operator/` on a machine you trust), and keep it on the air-gapped machine
alongside your keystore. `maxAttestationLifetimeHours` is optional and defaults to 720
(30 days) if omitted. The signer checks every field against the package and **fails
closed**: chain, controller, vault, auditor, and project ID/profile digest must match
exactly, and it independently enforces `now < validUntil` and `validUntil` falling within
the policy's maximum attestation lifetime of `metadata.createdAt` — a zero, already-expired,
or far-future `validUntil` is a hard rejection, not just a line on the review screen for you
to notice.

There is deliberately no way to sign without a policy file, and no way to skip the
interactive confirmation (`--yes`) without also passing `--unsafe-test-mode` — a scripted
or automated invocation must explicitly acknowledge that it is bypassing the human review
step; `--policy` is still fully enforced either way. `--yes`/`--unsafe-test-mode` exist for
scripted testing/CI only and should never appear in a real signing session.

The signer will:

1. safely extract the package and verify every file's hash against `manifest.json`, and
   that the package's actual contents are *exactly* the files `manifest.json` declares —
   no extra, unaccounted-for payload can ride along;
2. validate `profile.json` and `metadata.json` against the platform's fixed schemas and
   the project's own Asset Profile;
3. recompute the profile/metadata canonical digests, the record key (mint) or read the
   operation ID (burn), and the EIP-712 digest — all independently, from the raw files,
   never trusting a value the package merely *claims*;
4. cross-check that `metadata.issuance.amount` exactly equals the typed-data `amount`,
   and that `metadata.json`'s project/unit match `profile.json`'s;
5. check chain ID, controller, vault, auditor, project ID, profile digest, and the
   `validUntil` window against your `--policy` file — a mismatch on any of these is a
   hard failure;
6. print a review screen of every security-critical field plus the project's configured
   `displayFields`;
7. require you to type `y` or `yes` — anything else, including a blank line, is treated as
   "no" (fail closed);
8. sign only after your confirmation, and only if the resulting signature actually
   recovers to the auditor address you (or the package) declared — if your keystore key
   does not match, it refuses to write a signature that would be useless on-chain anyway.

If any step fails, the signer exits non-zero, prints the reason, and writes nothing. A
partially-reviewed package never produces a `signed-result.json`.

## 4. What to check by hand

The signer proves the package is *internally consistent* and *correctly bound* to this
chain/contract/project. It cannot know whether the record is *true*. Before confirming:

- **Match the record to your own evidence.** Compare every `displayFields` value (and, for
  anything not surfaced there, open `metadata.json`'s `asset` object directly) against the
  physical audit report, custodian statement, or other source-of-truth document for this
  specific `recordId`.
- **Check proof file lines carefully.** The review screen marks each `metadata.proofs[]`
  entry either matched to an attached file or flagged `NO ATTACHED FILE MATCHES THIS
  HASH`. A hash with no file is not automatically disqualifying (hash-only proofs are
  allowed), but it means you are trusting a hash you cannot independently verify
  from the package alone — go verify that document through another channel before signing.
- **Sanity-check the amount and unit.** `amount` is always in the token's smallest unit
  (e.g. 18-decimal wei-equivalent for an 18-decimal token); confirm it matches what the
  physical record justifies once you convert units, not just that the digits "look right".
- **`validUntil` is enforced, not just displayed.** The signer refuses to sign a zero,
  already-expired, or far-future `validUntil` (beyond your `--policy` file's
  `maxAttestationLifetimeHours`, 30 days by default) before you ever
  see the review screen. If signing succeeds, `validUntil` already passed this check; still
  glance at it, since a `validUntil` right at the edge of the window is a sign something is
  off with how the package was generated even though it technically passed.
- **For burns specifically**, additionally confirm this is a legitimate de-tokenization
  (backing genuinely removed) and not an attempt to burn tokens that should instead have
  gone through `RedemptionEscrow`. `SupplyController.burn` only touches Vault inventory —
  confirm with the operator, out of band, that the Vault balance you're told corresponds to
  unsold inventory and not tokens someone expects back later.
- **One record, one signature.** If you are ever asked to re-sign the same `recordId` "just
  in case", stop and ask why — the contract rejects a reused record key, so a legitimate
  workflow never needs a second signature for the same mint record. A request to re-sign is
  a strong signal of either an operational bug or a social-engineering attempt.

## 5. Key management

- **Hardware-backed signing is the recommended production configuration** for the auditor key
  (`--hardware ledger|trezor|yubikey|hsm`).
  No device has a working integration in the current build yet, so every production deployment
  today uses the hardened software-keystore fallback described below; treat that as an
  explicitly-approved interim state, not the intended end state, and move to hardware once your
  platform operator has a supported device wired up.
- Your keystore password is never logged, printed, or written to disk by the signer
  (`signer/internal/keystore`); treat a prompt asking you to paste it anywhere else as
  suspicious.
- The interactive prompt is the default and safest option: it hides your input (no
  terminal echo) whenever stdin is a real terminal. Prefer it over `--password-file` or
  `--password` for routine signing — a hidden prompt leaves no extra copy of the password
  on disk.
- `--password-file` is available for automation (e.g. a scripted CI signing job), not as
  a safer default: it puts a plaintext copy of the password on disk, so if you use it,
  the file must be owner-read-only (`chmod 600`) — the signer refuses to read a group- or
  world-readable password file — stored only on the same protected medium as the keystore,
  and deleted once you're done signing.
- `--password` (the password directly on the command line) is deprecated and prints a
  loud warning every time it's used: it is visible in process listings and shell history
  for as long as those persist. Avoid it outside throwaway test environments.
- **Creating a keystore**: use `signer keystore create --out <path>` (generates a brand-new key)
  or `signer keystore import --privkey-file <path> --out <path>` (encrypts a key you already
  hold, read from an owner-only-readable file — never pass a private key as a command-line
  argument). Both write this signer's own Argon2id + AES-256-GCM format
  (`signer/internal/keystore/native.go`) and reject any new password shorter than 12 characters
  or matching a small list of known-weak placeholders. There is deliberately no "export"
  subcommand: the only way key material ever leaves the keystore file is as a signature over a
  digest, via `signer sign`.
- **KDF strength is enforced on every unlock, not just at creation.** A keystore file (either
  format) whose key-derivation parameters have been downgraded below policy — by an older tool
  version or by tampering — is refused before your password is even tried; the error tells you to
  re-encrypt with `signer keystore create`. If you have an existing Ethereum V3 keystore from
  another tool with weak parameters (e.g. `geth`'s "light" scrypt profile), it will not open here
  until it is re-encrypted at policy strength.
- **Repeated wrong passwords lock the keystore out temporarily.** The first few mistyped attempts
  are free; further failures trigger a growing lockout (capped at one hour) recorded next to the
  keystore file (`<keystore>.throttle.json`). This is expected behavior, not a bug — if you are
  legitimately locked out, wait for the lockout to expire; there is no override.
- **Every unlock attempt is recorded in a tamper-evident, hash-chained audit log**
  (`<keystore>.auditlog.jsonl`, next to the keystore file) — success, failure, and the resulting
  signature's digest, never your password or private key. Keep this file with your other signing
  records; it lets you (or the platform admin, during an incident review) verify no entry was
  altered after the fact. `signer sign` re-verifies this log's entire chain before every unlock and
  refuses to proceed if it's broken — a signing attempt that would extend a corrupted trail is
  rejected outright rather than silently adding a fresh-looking entry on top of it.
- **The audit log has a companion anchor log** (`<keystore>.auditlog.anchor.jsonl` by default,
  override with `--audit-anchor <path>`) recording the newest entry's sequence number and hash
  each time one is written. This is what catches wholesale *truncation* of the main log's newest
  entries — a hash chain alone proves no *surviving* entry was altered, but says nothing about
  entries that were deleted outright, since there's nothing left to contradict. **The default
  anchor location is on the same disk as the keystore, which only catches a careless or partial
  truncation** — an attacker with full write access to that disk can rewrite both files together.
  For real truncation resistance, point `--audit-anchor` at genuinely separate media (a second USB
  drive, a location on a different filesystem) that only the auditor writes to.
- Every one of the writes above — the audit log entry, the anchor entry, and clearing the
  throttle lockout on a successful unlock — is durably synced (`fsync`) before `signer sign`
  proceeds, and outside `--unsafe-test-mode` (i.e. in normal, non-scripted use) a signing attempt
  is **aborted, producing no `signed-result.json`, if any of those writes fails** — e.g. the
  medium holding the keystore is full or has gone read-only. A signature is never released without
  a durable record that it was produced.
- Back up the keystore file itself (encrypted, offline, ideally geographically separated)
  — losing it does not put funds at risk (mint/burn is authorization, not custody), but it
  does mean the platform admin must rotate to a new auditor, which is a governance action
  with its own delay.
- If you believe your key or password may have been exposed, tell the platform admin
  immediately so they can rotate the auditor (see `docs/security/incident-response.md`,
  "Compromised auditor key"). Rotating the configured auditor immediately invalidates every
  not-yet-submitted signature from the old key (`SupplyController.mint`/`burn` check
  `attestation.auditor == auditor` against the *current* configured value), so prompt
  rotation is the actual containment step — not just changing your local password.

## 6. After signing

`signed-result.json` (schema: `shared/schemas/signed-result.schema.json`) contains
your address, the attestation type, the digest you signed, your signature, and a
timestamp — nothing sensitive. Transfer it off the air-gapped machine and hand it back to
the admin, who broadcasts it on-chain from their own wallet (the server does not relay it).
You do not need network access to produce it, and whoever submits it does not need your key
— `mint`/`burn` are permissionless to call once a valid signature exists.
