# Incident response guide

Audience: issuer/operator security team. Covers detection, containment, and recovery for
the key-compromise and system-failure scenarios this platform's trust model explicitly
anticipates, plus the honest limits of what any response can undo.

This guide assumes the role/mutability matrix in `docs/spec/roles.md`. Read that first if
anything below seems to promise a recovery path that doesn't exist — V1 has **no clawback, no
force-transfer, and no wallet-recovery module** (all explicitly deferred); several incident types
below are contained but not fully reversed on-chain.

## 0. General principles

- **Pause first, investigate second**, whenever an incident could plausibly affect a
  state-changing operation still in flight. `RWAToken.paused()` is the single project-wide
  emergency flag and blocks `SupplyController`, `Vault`, and `RedemptionEscrow` state
  changes (`docs/spec/contracts.md`, "Project pause"). It does not undo anything already
  final on-chain.
- **Rotation beats deletion.** For every compromised role, the correct first action is an
  admin-executed rotation (new key/address takes over the role), not merely disabling the
  old key locally. On-chain state only reflects the *current* role holder.
- **Every admin action should be a multisig transaction**, not a single hot key — the admin
  should be a multisig in production. If your deployment's `DEFAULT_ADMIN_ROLE` is not currently
  a multisig, treat that as a standing finding (see `docs/security/security-review.md`) and
  prioritize fixing it before anything else on this page, since every containment step below
  assumes admin actions require multiple approvals — a single compromised admin key defeats them
  all.
- **Log everything you do.** `auditlog` and on-chain events are the only source of truth
  once state has diverged; write down timestamps, transaction hashes, and who authorized
  each step as you go, for the postmortem.

## 1. Compromised auditor key

**Detection:** unexpected `Minted`/`Burned` events with a `recordId`/`operationId` your
records don't recognize; the auditor reports a lost/stolen key or device; a signed
attestation surfaces that no one on the audit team remembers signing.

**Containment:**
1. Admin executes `SupplyController` auditor rotation to a new address (emits
   `AuditorChanged`). Do this *immediately* — it is the actual fix, not a follow-up step.
2. Rotation has an important built-in property: `mint`/`burn` check `attestation.auditor ==
   auditor` against the **current** configured value (`docs/spec/contracts.md` check #2).
   The instant you rotate, every not-yet-submitted signature from the old key — no matter
   how many are outstanding, and even if you don't know how many exist — becomes
   permanently unusable. You do not need to track down or "cancel" individual signatures.
3. Pause the project (`PAUSER_ROLE`) if you cannot rotate the auditor immediately (e.g.
   waiting on multisig quorum), to stop any relay of a suspect signature in the interim.
4. Issue the outgoing auditor a fresh keystore and password for any future signing; treat
   the compromised keystore file as permanently burned, not reusable after a password
   change.

**What this does NOT undo:** any mint/burn already relayed and mined before you rotated.
If a fraudulent mint already landed, there is no burn-back-to-zero shortcut — the tokens
exist and are subject to the same permissioned-transfer rules as any other supply. Recovery
is an off-chain/legal matter (recover the tokens through negotiation, or the issuer
initiates a compensating, properly-audited burn from Vault inventory if the tokens are
recovered there). Document this limitation to stakeholders honestly and immediately;
overstating an on-chain "undo" capability the platform doesn't have is worse than admitting
the gap.

**Postmortem:** how was the key exposed (device compromise, password reuse, physical
theft)? Was the air-gapped machine actually air-gapped? Update `docs/auditor/auditor-guide.md`
key-hygiene guidance if the exposure vector reveals a gap in it.

## 2. Compromised compliance key (`COMPLIANCE_ROLE`)

**Detection:** unexpected `StatusChanged` events; a previously-blocked address becomes
`Allowed` (or vice versa) without a corresponding KYC/webhook record; compliance-operator
reports credential compromise.

**Containment:**
1. Admin revokes `COMPLIANCE_ROLE` from the compromised key and grants it to a new one.
2. Review `StatusChanged` events since the suspected compromise window; for any wallet
   status change you cannot attribute to a legitimate KYC event, revert it with a new,
   correctly-attributed `setStatus` call.
3. This role **cannot mint, burn, or move funds** — its blast radius is limited to
   who is allowed to transact: a compromised compliance operator can approve or block wallets
   but cannot mint. A compromised compliance key is serious (it can wrongfully freeze legitimate
   holders or wrongfully allow a sanctioned/unverified wallet in) but is not a funds-at-risk
   incident by itself.
4. If the key is the KMS-backed server hot key, rotate the KMS credential and audit the
   webhook signature-verification path (`server/internal/compliance/webhook.go`) for whether
   the compromise came through a webhook HMAC bypass rather than direct key theft — those
   require different fixes.

**Never set the Vault or RedemptionEscrow address to `Blocked`, under any circumstance,
including "just to be safe" during an incident.** `RWAToken._update` requires both `from`
and `to` to be `Allowed` on an ordinary transfer, and `Vault`/`RedemptionEscrow` are both
`from` or `to` on every buy and claim. Blocking either one doesn't contain an
incident — it self-inflicts a platform-wide outage and, worse, can strand a `Funded`
redemption's escrowed quote mid-claim (the claim transfer to Vault reverts, so the request
never reaches `Completed`). As of ADR-001 (`docs/adr/ADR-001-compliance-system-address-protection.md`),
`ComplianceRegistry` pins both system addresses `Allowed` for the life of the deployment and
rejects any attempt to change that at the contract level — but treat this note as
defense-in-depth, not a reason to stop being careful with batch `setStatuses` calls during
an incident.

## 3. Compromised admin (multisig signer or, worse, a non-multisig admin key)

**Detection:** unexpected role grants/revocations; unexpected auditor/treasury/strategy
changes; a multisig signer reports device compromise.

**Containment:**
1. If admin is a proper multisig: the remaining honest signers rotate out the compromised
   signer's key via the multisig's own signer-management process (outside this platform's
   contracts) before quorum can be reached by an attacker plus any other compromised
   signer. Do this before anything else — every other containment step in this document
   assumes admin integrity.
2. If admin is (against recommendation) a single EOA and it's compromised: this is the
   worst-case incident in this platform's trust model. The compromised admin can rotate
   auditor, treasury, redemption manager, and strategy, and can pause/unpause. There is no
   on-chain admin-recovery path in V1 (no timelock veto, no social recovery). Immediate pause
   buys time only if the attacker hasn't already unpaused or doesn't control `PAUSER_ROLE`
   too. Treat this as an emergency requiring off-chain/legal escalation and likely a new
   deployment once contained — this is precisely why the admin should be a multisig in
   production.
3. `AccessControlDefaultAdminRules`'s admin-transfer delay (≥24h recommended in production)
   is your main structural defense here: a malicious `DEFAULT_ADMIN_ROLE` transfer cannot
   complete instantly. Monitor `DefaultAdminTransferScheduled`-style events and treat any
   unexpected one as an active incident, not a notification to file away.

## 4. Redemption not funded promptly / funding-SLA incident

This is an *operational*, not necessarily *security*, incident, but it belongs here because
mishandling it looks like a security failure to holders.

**Detection:** `RedemptionRequested` events with no corresponding `RedemptionFunded` event
approaching `redemptionTimeout`.

**Response:**
1. This is not a bug to "fix" on-chain — a redemption request is deliberately not guaranteed
   to be funded, and V1 has no pooled/instant-redemption fallback. The correct response is
   operational: fund it, communicate a delay honestly to the holder, or let it time out so the
   holder can `cancelRedemption` and recover their RWA tokens.
2. Never describe a `Pending` request as funded, and never let it silently lapse without
   holder communication — the UI must distinguish Pending, Funded, and Claimable clearly.
3. Track this as an SLA metric (pending-redemption SLA alerts), not merely a support ticket
   queue.

## 5. Smart-contract bug discovered post-deployment

**Containment:**
1. Pause immediately (`PAUSER_ROLE`) if the bug is exploitable in a live, funds-affecting
   way. Pausing stops `SupplyController`, `Vault`, and `RedemptionEscrow` state changes but
   does **not** stop plain ERC-20 `transfer`/`transferFrom` between already-allowed
   holders — `RWAToken._update`'s compliance check is independent of `paused()`. If the bug
   is in the token's transfer/compliance logic itself, pausing alone will not contain it;
   you may additionally need `COMPLIANCE_ROLE` to block specific wallets.
2. All V1 contracts are non-upgradeable — there is no proxy upgrade to patch the bug in
   place. Remediation is: pause to stop further damage, quantify the damage precisely from
   on-chain events, and deploy a new project version via `RWAFactory` once the fix is audited.
   Migrating holder balances/state to the new deployment is a manual, off-chain-coordinated
   process this platform does not automate.
3. Do not attempt an unaudited hot-fix under pressure. The non-upgradeable, audited-deploy
   discipline exists precisely because a same-day patch is exactly how a second bug gets
   introduced.

## 6. Indexer/database diverges from chain state

**Detection:** `make ci`'s reconciliation checks fail, or an operator notices a UI value
that doesn't match a block explorer.

**Response:** the indexer is designed to be authoritative-from-chain, not
authoritative-from-database: reconstruct state from chain events when database records
diverge, and derive redemption status only from chain events — server workflow records may
annotate but never override chain state. The fix is a targeted reindex from a known-good
checkpoint, not a manual database edit. Manually editing read-model rows to "fix" a
discrepancy without also fixing the indexing bug that caused it will silently recur.

## 7. Quote-token anomaly (blacklist, pause, or failure)

**Detection:** a `buy`, `fundRedemption`, or `claimRedemption` call reverts unexpectedly
against the configured quote token.

**Response:** this is a known, accepted risk class: the quote token itself may blacklist an
address or fail, which can delay a funded claim independently of this platform. A funded
redemption remains funded and claimable — retry after the underlying quote-token issue
resolves (e.g. the beneficiary's address is unblacklisted by the token issuer). This is not
a platform bug to patch; communicate the dependency to the affected holder.

## 8. Escalation matrix

| Incident class                         | First responder                | Requires admin multisig action |
| --------------------------------------- | ------------------------------- | ------------------------------- |
| Compromised auditor key                 | Security on-call                | Yes (rotate auditor)            |
| Compromised compliance key              | Security on-call                | Yes (rotate role)               |
| Compromised admin signer                | All remaining multisig signers  | Yes (signer rotation)           |
| Redemption funding SLA breach           | Treasury/operations             | No (operational)                |
| Smart-contract bug (funds at risk)      | Security on-call + eng lead     | Yes (pause)                     |
| Indexer/DB divergence                   | Platform engineering            | No                               |
| Quote-token anomaly                     | Treasury/operations              | No                               |

Every row above ultimately needs a documented postmortem: timeline, root cause, on-chain
transactions involved, what was and was not recoverable, and any guide (this one, the
auditor guide, or `docs/security/security-review.md`) that should be updated as a
result.
