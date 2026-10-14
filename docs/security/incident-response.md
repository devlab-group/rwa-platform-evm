# Incident response guide

Detect, contain and recover from key compromises and platform failures using this guide
and the [role matrix](../spec/roles.md). Contracts are non-upgradeable and have no wallet
recovery module. Admin freezing and forced transfers (`setFrozenTokens`, `forcedTransfer`)
can contain incidents but cannot reverse completed mints, purchases or payouts.

## 0. General principles

- Pause promptly when an incident threatens further state changes. `RWAToken.pause()` requires
  `PAUSER_ROLE`, initially held by the admin. It stops RWA transfers, mint, burn, purchases,
  proceeds withdrawals and redemption transitions except timeout cancellation. Compliance,
  freezing, forced transfers, auditor and price changes, and role administration remain available.
  See [Project pause](../spec/contracts.md) for the full matrix. Pause does not
  undo completed transactions.
- Replace compromised authority: grant required roles to trusted replacements and revoke the
  old holders. The Security screen supports role changes, pause, freezing and forced transfers.
  For a Safe role holder, it exports a Safe{Wallet} Transaction Builder batch for owner approval
  ([operator guide §4a](../operator/operator-guide.md#4a-privileged-roles-and-administration)).
- Use a multisig admin in production. Administrative containment assumes a trusted approval
  quorum; a single key holding `DEFAULT_ADMIN_ROLE` defeats that assumption.
- Route alerts to your paging system. The server emits logs, audit entries and
  `rwa_alerts_fired_total{kind}`, with no outbound notifier
  ([operator guide §5](../operator/operator-guide.md#5-monitoring-and-alerts)). Role, auditor and
  admin-transfer changes have no dedicated alert; monitor their events and the Security screen.
- Record timestamps, transaction hashes and authorizations. Preserve on-chain events and the
  MongoDB `audit_logs` collection; `opsctl` entries use category `opsctl`.

## 1. Compromised auditor key

Detection: an unapproved `Minted` record key or `Burned` operation ID, a lost or stolen signing
key/device, or an attestation the auditor cannot account for.

1. Call `SupplyController.setAuditor(newAuditor)` from the admin wallet or Safe. It works while
   paused and emits `AuditorChanged`; the console has no form for it.
2. Once rotation takes effect, mint and burn reject unsubmitted attestations naming the old
   auditor. There is no need to revoke signatures individually.
3. Pause if rotation must wait for quorum. Anyone with a valid attestation can submit it;
   both supply operations revert while paused.
4. Provision a fresh keystore and update the signer's `auditor` policy field. Changing the
   compromised keystore's password does not restore trust in the key.

For a Safe auditor, fewer compromised owners than its threshold cannot authorize alone.
Replace compromised owners through a trusted Safe quorum and update every owner's `safe.owners`
and threshold policy. Removed owners no longer count at verification. Rotate the auditor
address if its approval threshold may be compromised.

Rotation cannot undo a mined mint or burn. Fraudulent unsold supply remains in the Vault: pause
purchases and prepare a burn signed by the new auditor. Burn also requires an unpaused project;
have the Safe holding `PAUSER_ROLE` execute unpause, burn and re-pause in one atomic Transaction
Builder batch, leaving no intervening purchase window. Sold supply belongs to buyers who paid
for it; recovery requires a legal decision. The admin can force-transfer it to the Vault for
burning. Replacing fraudulently burned inventory requires a new mint attestation. Tell
stakeholders what recovery can achieve.

Investigate device compromise, password reuse, physical theft and whether the signing machine
was offline. Update [`docs/auditor/auditor-guide.md`](../auditor/auditor-guide.md) if the review
finds a custody gap.

## 2. Compromised compliance key (`COMPLIANCE_ROLE`)

Detection: unexplained `StatusChanged` events, allowlist changes without a KYC or manual record,
or reported credential exposure. The event identifies its caller.

1. Grant a replacement `COMPLIANCE_ROLE` and revoke the compromised holder. For the server,
   provision the replacement through `keys.provider_mode` (`vault` or `local-keystore`), restart
   with it, then revoke the old key
   ([operator guide §7](../operator/operator-guide.md#7-key-rotation)).
2. Review events from the suspected compromise time and correct unauthorized statuses with
   new `setStatus` calls.
3. Check related holdings. A wrongly Allowed wallet can buy and receive RWA; a blocked wallet
   cannot transfer or progress an unfunded redemption. A Funded claim remains payable despite
   registry blocking. The compliance role itself cannot mint, burn or move tokens; an admin can
   freeze or force-transfer a non-system holder's tokens when required.
4. Check webhook-secret exposure separately: `security.kyc_webhook_hmac_secret`,
   `kyc.sumsub.webhook_secret` or `kyc.onfido.webhook_token` can authenticate forged decisions for
   wallets with ownership proof. Rotate the affected secret in both provider and server settings.

Vault and RedemptionEscrow are pinned system addresses. Only Allowed with no expiry is accepted
for them (`SystemAddressCannotBeBlocked`). An attempted block in `setStatuses` reverts the
whole batch; exclude them from blocking lists.

## 3. Compromised admin

Detection: unexpected `RoleGranted`, `RoleRevoked`, `AuditorChanged`, `TreasuryChanged`,
`StrategyChanged`, `Frozen`, `ForcedTransfer` or `DefaultAdminTransferScheduled` events;
`config_drift` after wiring changes; or a compromised multisig owner's device.

An admin controls each contract where it holds `DEFAULT_ADMIN_ROLE`, initially all six. It can
grant operational roles, replace the auditor to authorize supply changes, redirect future
proceeds, replace the Vault strategy, freeze non-system holders or force-transfer their tokens
to Allowed recipients.

1. If trusted Safe owners can meet the threshold, replace compromised owners through the Safe.
   If they cannot form a trusted quorum, administrative containment cannot proceed normally.
2. A compromised threshold or single-key admin has no platform recovery path: no timelock veto,
   social recovery or upgrade. Pause gives limited protection because the admin can grant itself
   `PAUSER_ROLE` and unpause. Escalate through legal and other off-chain channels and plan a
   replacement deployment once the compromise is contained.
3. The admin-transfer delay protects transfer of the role, not actions by its current holder.
   Each contract requires `beginDefaultAdminTransfer`, the delay, then
   `acceptDefaultAdminTransfer`; the current admin may cancel before acceptance. Use at least
   24 hours in production. Treat unexpected `DefaultAdminTransferScheduled` as an incident.
   The indexer and Security screen expose it, but no dedicated alert fires.

## 4. Compromised operational role

Revoke the compromised key's roles on every hosting contract and grant them to trusted replacements.

| Role | What a compromised holder can do | Response |
| --- | --- | --- |
| `PAUSER_ROLE` | pause indefinitely or unpause during containment | revoke; the admin initially holds the role |
| `PRICER_ROLE` | sell inventory cheaply through a low purchase price, or commit inflated quotes through a high redemption price | pause and reset both prices; prepare rejection of inflated Pending requests, then unpause to reject (`REDEMPTION_MANAGER_ROLE`); completed purchases remain final |
| `TREASURER_ROLE` (Vault and RedemptionEscrow) | withdraw proceeds to configured `treasury`; fund Pending requests from its own quote balance | revoke on both contracts |
| `REDEMPTION_MANAGER_ROLE` | reject Pending requests and return their RWA | replace the role holder; affected investors can request again |

## 5. Compromised server or server secrets

The server relays compliance writes with its compliance key (section 2). Admin, pricer and
treasurer transactions use operator wallets or Safes; the server holds none of those keys.
Protect the configuration YAML as a secrets file:

- `security.jwt_secret` permits forging admin sessions, including access to manual compliance
  writes. Rotation invalidates existing sessions.
- `security.admin_address` selects the wallet allowed to log in.
- Provider webhook secrets and key-provider credentials (`keys.vault_token`, or the keystore
  and `keys.keystore_password_file`) protect compliance authority.

Rotate exposed secrets and restart. Rotate the compliance key if the host may be compromised.
Treat the served admin console as untrusted until the host is rebuilt; independently check wallet
prompts for target contract, function and arguments.

## 6. Redemption not funded promptly

Detection: `pending_redemption_sla` (default 48 hours),
`rwa_redemptions_pending_oldest_age_seconds`, or Pending requests nearing `redemptionTimeout`.

1. Fund the request, explain the delay, or let it time out so the compliant beneficiary can
   cancel and recover RWA. Funding is not guaranteed; V1 provides no pooled or instant redemption.
2. Never describe Pending as funded or let a request lapse without telling its holder.
   Distinguish waiting for funding from a Funded request eligible for claim.
3. Track the delay as an SLA breach as well as a support issue.

## 7. Smart-contract bug discovered after deployment

1. Pause immediately if the bug threatens funds. Pause blocks ordinary RWA transfers, supply
   operations, purchases and most redemption transitions. For affected paths that remain
   available, such as timeout cancellation, compliance or enforcement, restrict their applicable
   roles or wallet eligibility instead; consult the pause matrix.
2. Deployed contracts cannot be patched in place. Stop further damage, quantify it from events and
   deploy a corrected, audited version as a new project. Balance and state migration is manual;
   the platform does not automate it.
3. Review and audit the fix before deployment, even under incident pressure.

## 8. Indexer or database diverges from chain state

Detection: `config_drift` with `rwa_config_drift` = 1, a halted `ReconciliationRequired`
checkpoint after an unrecoverable reorg, rising `rwa_read_model_reconcile_errors_total`, the
Security screen's stale warning, or disagreement with a block explorer.

Rebuild from the authoritative chain rather than editing read models. Redemption status follows
chain events; server annotations do not override it. Use
[operator guide §6](../operator/operator-guide.md#6-backup-and-recovery):

- `opsctl indexer reset-checkpoint --trusted-block=<n> --trusted-hash=0x...` to recover from
  `ReconciliationRequired`, using an independently verified block;
- `cmd/reindex` to drop indexed events and rebuild read models;
- `opsctl dlq list|inspect|retry|dismiss` for undecodable logs.

## 9. Quote-token anomaly (blacklist, pause, fee or failure)

Detection: quote-token failures in `buy`, `withdrawProceeds`, `fundRedemption` or
`claimRedemption`, or `funded_claim_failure`. `QuoteDeltaMismatch` reports an unexpected transfer
amount, including fees; blacklist or pause failures use the quote token's revert.

Quote tokens can fail independently of the platform. A failed claim leaves the request Funded;
anyone can retry after the token issue is resolved. The payout cannot be redirected, so a
blacklisted beneficiary needs the quote-token issuer to restore eligibility. Explain that
dependency to the holder.

Check eligibility before funding and combine funding with claim in one atomic batch to avoid
committing funds when payout fails. See
[operator guide §2a](../operator/operator-guide.md#2a-quote-tokens-with-an-allowlist-or-a-blacklist)
and the [redemption guide](../operator/redemption-ops.md).

## 10. Escalation matrix

| Incident | First responder | Admin multisig action |
| --- | --- | --- |
| Compromised auditor key | security on-call | yes: rotate the auditor |
| Compromised compliance key | security on-call | yes: rotate the role |
| Compromised admin owner | all remaining multisig owners | yes: replace the owner |
| Compromised operational role | security on-call | yes: rotate the role |
| Compromised server or secrets | platform engineering and security on-call | only to rotate the compliance key |
| Redemption funding SLA breach | treasury and operations | no |
| Smart-contract bug, funds at risk | security on-call and engineering lead | yes: pause |
| Indexer or database divergence | platform engineering | no |
| Quote-token anomaly | treasury and operations | no |

Write a postmortem with the timeline, cause, transactions, recovery limits and required updates
to this guide, the operator guide or auditor guide.
