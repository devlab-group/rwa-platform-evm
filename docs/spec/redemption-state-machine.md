# Redemption state machine

Status: `None → Pending → {Funded → Completed | Rejected | Cancelled}`.

| From    | Function          | Caller                  | Guard                                              | To        | Asset effect                                   |
| ------- | ----------------- | ----------------------- | -------------------------------------------------- | --------- | ---------------------------------------------- |
| None    | requestRedemption | anyone allowed          | !paused, amount>0, deadline ok, quote>=minQuoteOut | Pending   | pull exact rwaAmount from caller into escrow   |
| Pending | fundRedemption    | TREASURER_ROLE          | !paused, beneficiary allowed                       | Funded    | pull exact quoteAmount from funder into escrow |
| Pending | rejectRedemption  | REDEMPTION_MANAGER      | !paused, reasonCode!=0, beneficiary allowed        | Rejected  | return exact rwaAmount to beneficiary          |
| Pending | cancelRedemption  | beneficiary only        | now >= createdAt + timeout, beneficiary allowed    | Cancelled | return exact rwaAmount to beneficiary          |
| Funded  | claimRedemption   | anyone (permissionless) | !paused                                            | Completed | rwaAmount → Vault; quoteAmount → beneficiary   |

Invariants:
- A request's RWA leaves escrow exactly once.
- A funded request's quote is paid exactly once, only to the recorded beneficiary.
- Completed sends RWA to Vault inventory and quote to beneficiary; total supply unchanged.
- Reject/Cancel pay no quote and return exact RWA.
- No role can withdraw funded quote (no such function exists).
- No partial funding/claim, no batching, no fees.
- Funding is irreversible.
- `requestRedemption` snapshots `quoteAmount = strategy.quoteRedemption(rwaAmount)`.
- Claim does NOT re-check whitelist (compliance is enforced at funding time).
- If beneficiary no longer allowed, reject/cancel revert (RWA stays escrowed).

Every transfer verifies the exact balance delta. `redemptionTimeout` default 14 days,
immutable per deployment, factory-bounded (e.g. 1 day … 365 days).

## Footnote: pause semantics

The Guard column lists an explicit `!paused` check on requestRedemption, fundRedemption,
rejectRedemption, and **claimRedemption**, but NOT on **cancelRedemption**. That asymmetry is
deliberate and load-bearing, not incidental:

- **cancelRedemption completes while paused.** A timed-out cancellation returns the
  beneficiary's *own* escrowed RWA (the redemption was never funded). Trapping that behind an
  indefinite emergency pause is an availability / incident-response weakness. cancelRedemption
  therefore omits the `!paused` guard AND no longer returns RWA via the pausable
  `token.safeTransfer`; it calls `RWAToken.returnEscrowedRWA`, a **narrow escrow-only** path
  that moves the escrow's own RWA to the beneficiary even while the token is paused. The bypass
  cannot become a general backdoor: only the wired RedemptionEscrow may call it, it always
  debits the caller's (escrow's) own balance — never an arbitrary `from` — it re-checks
  compliance on both legs (so a since-blocked beneficiary still cannot receive:
  `RecipientNotAllowed`), and it can neither mint, burn, nor move any third party's tokens.

- **claimRedemption remains blocked while paused.** It keeps its explicit `!paused` guard. A
  funded claim pays out value (the funded quote to the beneficiary and returns RWA to the
  Vault); an emergency pause is intended to hold payouts, and the beneficiary's quote is already
  secured in escrow, so there is no availability need to force it through. (Its RWA leg also
  calls `returnEscrowedRWA`, but only for the compliance-recheck/exact-return path — it is
  reached only when not paused, so claim's pause behavior is unchanged.)

Covered by `test_cancelRedemption_succeedsWhilePaused`,
`test_cancelRedemption_whilePaused_stillRequiresBeneficiaryCompliance`, and
`test_cancelRedemption_whilePaused_generalTransfersStillBlocked` (proving the bypass is not a
general transfer backdoor). The token-side mechanism is documented on the NatSpec of
`RWAToken.returnEscrowedRWA`.

## Compliance changes during redemption funding

Compliance status is read from the authoritative on-chain registry during the same
transaction that performs the state transition — there is no "atomicity within a block", only
each transaction's own execution point. Required semantics, all satisfied by the Guard column
above and confirmed by the tests named below:

- `requestRedemption` requires the caller allowed at its own execution time.
- `fundRedemption` re-checks the *recorded beneficiary* allowed at its own execution time —
  independent of whether the beneficiary was allowed when the request was made.
- Once `Funded`, no later compliance change can prevent payment of the committed quote:
  `claimRedemption` never re-checks compliance.
- `claimRedemption` always pays `request.beneficiary` as recorded at request time; no function
  on `IRedemptionEscrow` ever mutates that field after `requestRedemption` sets it once.
- A `Funded` request cannot be rejected, cancelled, or defunded (both `rejectRedemption` and
  `cancelRedemption` require `status == Pending`).

Because the contract only ever observes one final transaction ordering per block, the
outcome for any interleaving of `{request, whitelist removal, whitelist restoration, funding,
claim, timeout cancellation}` is fully deterministic from that ordering alone:

| Ordering (funding/removal/restoration relative to each other) | Outcome                                                           | Test                                                                               |
| ------------------------------------------------------------- | ----------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| removal before funding                                        | `fundRedemption` reverts `BeneficiaryNotAllowed`, stays `Pending` | `test_fundRedemption_beneficiaryDeWhitelistedReverts`                              |
| funding before removal                                        | funding commits; later removal cannot unwind `Funded`             | `test_fundRedemption_thenRemoval_staysFunded`                                      |
| removal, then restoration, before funding                     | funding proceeds normally                                         | `test_fundRedemption_reapprovalBeforeFunding_proceeds`                             |
| removal after funding, before claim                           | claim still pays the recorded beneficiary in full                 | `test_claimRedemption_doesNotRecheckWhitelist`                                     |
| funded request, any compliance state                          | reject/cancel attempts revert `NotPending`                        | `test_rejectRedemption_afterFundReverts`, `test_cancelRedemption_afterFundReverts` |

A reorg that changes which of two competing transactions (e.g. funding vs. removal) actually
landed first changes which row above applies, but not the determinism itself — the contract
still behaves per whichever ordering the canonical chain settles on. Re-deriving state after a
reorg is the server indexer's job, not something Solidity tests exercise.

### Accepted V1 trust limitation: pre-funding removal, and its recovery path

An issuer or compliance operator can observe a `Pending` request and remove the beneficiary
before `fundRedemption` executes, which blocks funding indefinitely while that status holds.
This is an accepted V1 governance limitation (a later version may add compliance reason codes,
independent compliance governance, or an appeal workflow), on the condition that all of the
following hold:

- The inability to fund is visible on-chain (`fundRedemption` reverts `BeneficiaryNotAllowed`;
  no silent failure).
- Compliance changes emit `IComplianceRegistry.StatusChanged` — auditable, indexable.
- The investor can recover the escrowed RWA via timeout `cancelRedemption` — **but recovery is
  gated on the same compliance check as funding**, not unconditional: `cancelRedemption`'s
  RWA-return leg goes through `RWAToken.returnEscrowedRWA`, which itself enforces the
  platform-wide invariant that a transfer requires the recipient currently Allowed. So while the beneficiary remains Blocked, timeout cancellation reverts too (`test_cancelRedemption_deWhitelistedBeneficiaryReverts`)
  — the RWA is not lost or claimable by the issuer, just not yet movable. The instant
  compliance restores the beneficiary, the same timed-out `cancelRedemption` call succeeds
  (`test_cancelRedemption_timeoutRecovery_impossibleWhileBlocked_succeedsOnceRestored`). This is
  symmetric with funding's own gate, not a new failure mode.
- The issuer can never retain both the RWA and the quote: while blocked, `fundRedemption`
  cannot run, so no quote is ever pulled from the treasurer — the RWA simply sits escrowed
  until compliance is resolved one way or the other.
- The admin panel must surface a `Pending` request whose beneficiary is currently not allowed
  as blocked-by-compliance, distinctly from an ordinary pending request awaiting funding
  (server/UI concern, tracked outside this document).
