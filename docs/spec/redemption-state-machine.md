# Redemption state machine

Status flow: `None → Pending`, then from Pending either `→ Funded → Completed`, `→ Rejected` or
`→ Cancelled`.

| From    | Function            | Caller                    | Guards, in order                                                                               | To        | Asset effect                                                       |
| ------- | ------------------- | ------------------------- | ---------------------------------------------------------------------------------------------- | --------- | ------------------------------------------------------------------ |
| None    | `requestRedemption` | an Allowed wallet         | not paused, caller Allowed, amount > 0, deadline not passed, quote > 0, quote >= `minQuoteOut` | Pending   | pulls exactly `rwaAmount` from the caller                          |
| Pending | `fundRedemption`    | `TREASURER_ROLE`          | status, not paused, beneficiary Allowed                                                        | Funded    | pulls exactly `quoteAmount` from the funder                        |
| Pending | `rejectRedemption`  | `REDEMPTION_MANAGER_ROLE` | status, not paused, `reasonCode != 0`, beneficiary Allowed                                     | Rejected  | returns `rwaAmount` to the beneficiary                             |
| Pending | `cancelRedemption`  | the beneficiary           | status, caller is beneficiary, timeout reached, beneficiary Allowed                            | Cancelled | returns `rwaAmount` to the beneficiary                             |
| Funded  | `claimRedemption`   | anyone                    | status, not paused                                                                             | Completed | `rwaAmount` to the Vault, exactly `quoteAmount` to the beneficiary |

The table lists body guards after role and reentrancy modifiers. Fund, reject and claim check
status (`NotPending` / `NotFunded`) before pause (`ProjectPaused`); cancel has no pause guard.
Request errors follow the listed order: `ProjectPaused`, `CallerNotAllowed`, `ZeroAmount`,
`DeadlineExpired`, `ZeroQuote`, `QuoteBelowMin`. Other guards use `ZeroReasonCode`,
`NotBeneficiary`, `TimeoutNotReached` and `BeneficiaryNotAllowed`.

Invariants:

- Request fixes `quoteAmount = strategy.quoteRedemption(rwaAmount)` and `msg.sender` as the
  beneficiary; neither changes later.
- Each request's RWA leaves escrow once. Funded quote can be paid once, to that beneficiary;
  no role can withdraw it and no defunding function exists.
- Completed returns RWA to Vault inventory and pays the quote without changing total supply.
  Rejected and Cancelled return the exact RWA and pay no quote.
- No partial funding, partial claims or fees. Each contract call handles one request; Safe
  transaction batches can combine calls.
- Claim does not recheck beneficiary compliance. Fund, reject and cancel require the beneficiary
  to be Allowed, so a Pending request whose beneficiary is not Allowed keeps its RWA escrowed
  until the beneficiary is Allowed again.

Funding and payout check exact quote-token balance deltas (`QuoteDeltaMismatch`), measured at
the escrow and beneficiary respectively. Request checks the RWA pull (`RwaDeltaMismatch`).
Returns through the platform token are not delta-checked: reject uses `safeTransfer`; cancel
and claim use `returnEscrowedRWA`.

`redemptionTimeout` is immutable. The contract has no default; the factory requires
`1 day <= redemptionTimeout <= 365 days`. `Deploy.s.sol` defaults to 14 days and the admin
console's Setup form to seven.

## Footnote: pause semantics

Timeout cancellation remains available while paused, allowing a compliant beneficiary to
recover never-funded RWA. It uses `RWAToken.returnEscrowedRWA`, which debits only the wired
escrow's balance and checks compliance at both ends. It cannot mint, burn or move another
holder's tokens. Cancel also requires the beneficiary to be Allowed (`BeneficiaryNotAllowed`).

The other four transitions remain blocked. Claim uses the same return path to send RWA to the
Vault, but checks pause before doing so; its committed quote remains escrowed during the pause.
See the token's NatSpec and `contracts.md` for the return path's restrictions.

Tests: `test_cancelRedemption_succeedsWhilePaused`,
`test_cancelRedemption_whilePaused_stillRequiresBeneficiaryCompliance`, and
`test_cancelRedemption_whilePaused_generalTransfersStillBlocked`.

## Compliance changes during redemption funding

Request checks the caller's registry status when it executes. Funding independently checks the
beneficiary's status at execution, regardless of their request-time status. Once Funded, a
registry change cannot revoke the committed quote: claim does not recheck beneficiary
compliance, and reject and cancel require Pending. Claim still depends on an unpaused project
and a successful quote-token payout.

Transaction order determines the outcome:

| Order                                     | Outcome                                                                     | Test                                                                               |
| ----------------------------------------- | --------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| removal before funding                    | `fundRedemption` reverts `BeneficiaryNotAllowed`; the request stays Pending | `test_fundRedemption_beneficiaryDeWhitelistedReverts`                              |
| funding before removal                    | funding commits; the later removal can't unwind Funded                      | `test_fundRedemption_thenRemoval_staysFunded`                                      |
| removal, then restoration, before funding | funding proceeds normally                                                   | `test_fundRedemption_reapprovalBeforeFunding_proceeds`                             |
| removal after funding, before claim       | beneficiary removal does not block the claim's full payout                  | `test_claimRedemption_doesNotRecheckWhitelist`                                     |
| Funded, any compliance state              | reject and cancel revert `NotPending`                                       | `test_rejectRedemption_afterFundReverts`, `test_cancelRedemption_afterFundReverts` |

A reorg may change transaction order and therefore the applicable outcome. The contract follows
the canonical chain; the server indexer rebuilds its derived state.

### Accepted limitation: removal before funding

Removing a Pending beneficiary from the allowlist blocks funding until their status is restored.
The platform accepts this limitation with these conditions:

- Funding visibly reverts `BeneficiaryNotAllowed`; compliance changes emit
  `IComplianceRegistry.StatusChanged`, which the server indexes.
- Timeout cancellation also requires compliance. While blocked it reverts
  (`test_cancelRedemption_deWhitelistedBeneficiaryReverts`); after restoration the same cancel
  succeeds (`test_cancelRedemption_timeoutRecovery_impossibleWhileBlocked_succeedsOnceRestored`).
  The RWA remains escrowed, and the issuer cannot seize it from the system address.
- No quote is pulled while funding is blocked, so the issuer does not retain both assets.
- The console's Redemptions screen distinguishes a beneficiary blocked by compliance from a
  Pending request awaiting funding.
