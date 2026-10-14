# Auditor guide: air-gapped signing with `signer`

Use this guide when you hold a deployment's auditor key or an owner key for a Safe auditor
(§7). It covers offline signing, the evidence you must review, and key management. The
[contract specification](../spec/contracts.md) defines the `SupplyController` checks and their
order. Report any conflict between that specification and this guide to the platform operator.

## 1. What you are attesting to

> Every increase in token supply requires a valid auditor signature bound to this chain, this
> `SupplyController`, this project profile, one metadata record, one amount, one Vault, and one
> unused nonce.

A `MintAttestation` authorizes anyone holding the signature to submit a transaction that mints
exactly `amount` tokens into the project's Vault. A `BurnAttestation` authorizes burning that
amount from Vault inventory. Neither operation can be undone; a burn cannot take tokens from a
holder's balance or `RedemptionEscrow`.

Your signature states that you reviewed the recorded backing and found it sufficient for this
supply change. It is public authorization, not encryption or a promise about future backing.
The contract checks the authorization; you must establish that the asset and evidence are real
before you confirm.

The contract rejects signatures for the wrong chain, controller, auditor, profile or Vault. It
also rejects expired attestations, used nonces, used record keys (mint) or operation IDs (burn),
zero amounts, burns above the Vault's balance, and minting or burning while the project is
paused. These checks cannot detect a false asset record.

## 2. Machine and binary hygiene

- Sign on a machine that never connects to a network. Signing makes no network requests;
  the machine's air gap enforces isolation.
- Check the binary's checksum before each use, especially after updates. Agree with the
  operator on the build process and a trusted channel for checksums. The repository has no
  binary release, checksum or signing workflow; `make signer` builds `signer/bin/signer` locally.
- Check and use a dedicated medium, such as a USB drive, to transfer `.rwa` packages and
  results. ZIP parsing rejects traversal, symlinks, duplicate names and excessive size or
  decompression, but incoming files still need careful handling.
- Store the keystore and password separately on the offline side, for example on disk and
  in a password manager or safe. Keep both off the package-transfer drive.

## 3. Running the signer

```
signer sign <package.rwa> --keystore <path-to-keystore.json> --policy <path-to-policy.json>
```

Every run requires `--policy`. Maintain this file independently of the package under review;
never derive it from a `.rwa` package or copy a policy supplied inside one.

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

Obtain these values from the operator through a trusted channel after verifying the deployment,
then keep the policy on the offline machine. The six deployment fields are required.
`maxAttestationLifetimeHours` defaults to 720 hours (30 days) when omitted or set to 0; unknown
fields are rejected. Safe policies also include the optional `safe` block described in §7.

The signer requires the package to match the policy's chain, controller, Vault, auditor, project
ID and profile digest. It also requires
`now < validUntil <= min(now, metadata.createdAt) + maxAttestationLifetime`.
It rejects zero, expired and overly long deadlines before displaying the review screen.

The signing sequence is:

1. Extract the package in memory and verify its manifest: every declared file must have the
   expected size and SHA-256, and no payload may be undeclared.
2. Validate the profile and metadata envelopes, then validate the metadata's asset against the
   profile's schema. The project IDs and issuance unit must agree.
3. Recompute the profile and metadata digests. Compare them with the manifest, and check the
   profile digest and project ID against the policy.
4. Cross-check the typed data: digests, attestation type and amount must agree with the package.
   Amounts are compared numerically, so leading zeros do not change the value. For a mint,
   verify the record ID and its derived record key.
5. Check the remaining deployment values and expiry against the policy, then compute the
   EIP-712 digest.
6. Display the critical fields, proofs and the profile's `displayFields` for your review.
7. Ask for confirmation. Only `y` or `yes`, ignoring case and surrounding whitespace, proceeds.
   A blank response declines.
8. Unlock the key, sign, and check that the key's address matches the declared auditor before
   writing `signed-result.json`. A Safe owner signs a share instead (§7).

`--yes` skips confirmation only when paired with `--unsafe-test-mode`. These flags are for tests
and CI; keep them out of real signing sessions. The policy checks still run with either flag.

Failures return a non-zero exit status and an error. Validation, declined confirmation and key
mismatch prevent the result from being written. A mismatch is detected after signing, so the
audit log can still contain the unlock and signature (§5). If writing the output fails, a
partial file may remain; treat it as unusable.

## 4. What to check by hand

The signer checks package consistency and deployment binding. Before confirming, review the
underlying evidence:

- Compare displayed asset values with your audit report, custodian statement or other
  evidence for this `recordId`. Inspect `metadata.json`'s `asset` for fields the screen omits.
- Review every proof. Attached documents are matched by hash; missing ones show
  `NO ATTACHED FILE MATCHES THIS HASH`. For a hash-only proof, obtain and verify the document
  through another channel before relying on it.
- Convert `amount` from minimal units using the token's decimals and check that the backing
  supports the quantity. An 18-decimal token uses the same scale as wei; the digits alone
  do not establish the amount.
- Review `validUntil` despite the enforced policy window. Ask the operator about deadlines
  unexpectedly close to its maximum.
- For burns, confirm legitimate removal of backing and that the tokens belong in unsold
  Vault inventory, rather than in `RedemptionEscrow` or owed back to a holder.
- Review and record the reason for signing the same record again. A used record key cannot
  mint twice, but an unused expired authorization may need replacing. Do not sign "just in
  case". Each Safe owner reviews the digest and signs their own share.

## 5. Key management

Hardware-backed signing is the recommended production setup, but the integrations are not
implemented: `--hardware ledger|trezor|yubikey|hsm` currently fails for every device. Until a
supported integration is available, agree with the operator on use of the software keystore.

Prefer the interactive password prompt for routine signing. It hides input when stdin is a
terminal and avoids a password file on disk. The signer does not log or print passwords; treat
requests to paste yours elsewhere as suspicious. `--password-file` supports automation but
stores the password in plaintext. If used, restrict the file to its owner (`chmod 600`), keep it
on protected offline media and delete it afterwards. `--password` is deprecated and warns on
every use because command-line passwords appear in process listings and shell history. Reserve
it for throwaway tests.

Create a key with `signer keystore create --out <path>`, or import an existing key with
`signer keystore import --privkey-file <path> --out <path>`. Import reads a hex key from a file
accessible only to its owner; never pass a private key as a command-line argument. Both commands
write the native Argon2id and AES-256-GCM keystore format and refuse an existing output path,
including a symlink. New passwords must contain at least 12 bytes and six distinct characters,
and must not be on the weak-password list. There is no export command; protect the keystore and
every existing copy of the key.

Every unlock checks KDF parameters before running key derivation. Weak or excessive parameters
are rejected in both native and Ethereum V3 keystores, including V3 files made with geth's
"light" scrypt profile. To retain such a key, decrypt it offline with its original tool and
import it here. `signer keystore create` makes a different key, which requires an auditor change.
The [signer README](../../signer/README.md#keys-and-secrets) lists the exact limits.

After three consecutive failed unlocks, the fourth starts a five-second lockout. Further
failures double it, up to one hour. The counter is stored in `<keystore>.throttle.json` and
cleared after a successful unlock. Wait for the lockout to end; there is no CLI override.

Keep the audit files with your signing records:

- `<keystore>.auditlog.jsonl` records successful unlocks and signatures in a hash chain. Failed
  unlocks are also recorded when the failure bookkeeping succeeds; errors there produce a
  warning. Signature entries contain the EIP-712 digest, or the Safe message hash for an owner
  share, never a password or private key. The signer verifies the existing chain before unlock.
- `<keystore>.auditlog.anchor.jsonl` records the sequence number and hash of each new entry. It
  can detect deletion of recent main-log entries when the anchor survives. The default anchor shares
  the keystore's storage, so an attacker able to rewrite both files can rewrite the history.
  Use `--audit-anchor <path>` for media protected independently from the main log. Separate
  storage helps only if the attacker cannot also modify it.

Audit and anchor appends are synced to disk before they return. In normal mode, failure to clear
the lockout state, record a successful unlock or record a signature aborts signing without
writing the result. `--unsafe-test-mode` allows those failures with warnings. The throttle file
itself is not synced like the audit logs.

Back up the encrypted keystore offline, preferably in a separate location. Losing access can
interrupt supply operations until the platform admin appoints a replacement auditor. If you
suspect exposure of the key or password, notify the admin immediately and follow
[incident response](../security/incident-response.md), "Compromised auditor key". Once the
auditor change takes effect on-chain, `SupplyController` rejects unsubmitted attestations naming
the old auditor. Changing the local password does not revoke signatures.

For a Safe owner key, also notify the other owners, replace the compromised owner on the Safe,
and update every owner's policy (§7). One owner key can authorize alone when the Safe's
threshold is 1; a higher threshold requires additional owners.

## 6. After signing

`signed-result.json` contains the format version, auditor address, attestation type, EIP-712
digest, signature and timestamp. Its format is defined in
`shared/schemas/signed-result.schema.json`. The file contains no secrets, but it carries the
authorization you just granted.

Transfer it to the admin. For a mint, the admin uploads it on the console's Assets screen and
submits `SupplyController.mint` from their wallet. The console has no burn screen; submit a burn
by calling `SupplyController.burn` directly. The server relays neither transaction. Signing
requires no network access, and submission requires no auditor key: both contract methods are
permissionless once their signature and other checks pass. Safe owners produce shares first;
`signer combine` creates the final result (§7).

## 7. When the auditor is a Safe multisig

For a Safe v1.3.0 or later with `CompatibilityFallbackHandler`, owners review the package on
their own offline machines and sign shares. At least the configured threshold of owners must
contribute to the final result. A threshold of 1 permits one owner to authorize alone. The
[signer README](../../signer/README.md#safe-multisig-auditor) explains the signature format.

1. Maintain a policy naming the Safe as `auditor`, with its current owners and threshold obtained
   from independently verified on-chain state:

   ```json
   {
     "chainId": "31337",
     "controller": "<SupplyController address>",
     "vault": "<Vault address>",
     "auditor": "<the Safe's address>",
     "projectId": "<this deployment's projectId>",
     "profileDigest": "<the currently-active profileDigest>",
     "safe": {
       "owners": ["<owner 1>", "<owner 2>", "<owner 3>"],
       "threshold": 2
     }
   }
   ```

   The signer rejects invalid, zero or duplicate owner addresses, an empty list, and a threshold
   outside 1 through the number of owners. It does not query the Safe. Update every policy when
   owners or threshold change. A stale policy can still produce a valid result if its signatures
   satisfy the Safe's current rules; the Safe enforces those rules at submission.
2. Run `signer sign` with your owner key. The usual checks and review apply, with additional Safe
   threshold, owner-list and message-hash fields. The signed hash binds the Safe address, chain
   and attestation digest. A key outside the policy's owner list is refused. The output is
   `signature-share-<your address>.json`, with the address in lowercase, next to the package;
   `--out` changes the path.
3. Collect the shares and combine them:

   ```
   signer combine --policy <path-to-policy.json> <share-1.json> <share-2.json>
   ```

   Shares contain no secrets. Anyone can combine them; no key is required. `combine` checks the
   Safe, chain, common attestation digest and type, recovers each owner, and requires at least
   the policy's threshold of distinct owners. It writes `signed-result.json` next to the first
   share, unless `--out` supplies another path, with the Safe as auditor and signatures in
   ascending owner-address order.

   `combine` does not reopen the package or repeat its deployment, amount, nonce or expiry
   checks. Keep the shares associated with the package every owner reviewed, then hand the final
   result to the admin as described in §6. The contract still checks the attestation on submission.
