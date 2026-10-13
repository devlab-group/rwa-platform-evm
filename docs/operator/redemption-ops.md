# Redemption operations guide

V1 redemptions pay the configured ERC-20 quote token to the investor. They do not deliver the
physical underlying asset. The state machine is
`None → Pending → {Funded → Completed | Rejected | Cancelled}`; see the
[redemption state machine](../spec/redemption-state-machine.md).

## Lifecycle

A project pause stops requests, funding, rejection and claims. A beneficiary can still cancel
a timed-out Pending request while paused, provided they remain Allowed.

1. Request: an Allowed investor approves `RedemptionEscrow` to spend the RWA, then calls
   `requestRedemption(rwaAmount, minQuoteOut, deadline)`. The contract fixes
   `quoteAmount = strategy.quoteRedemption(rwaAmount)`, checks the minimum quote and deadline,
   and escrows the exact RWA amount. The request becomes Pending. Show this as a request,
   without promising payment.
2. Fund: a holder of `TREASURER_ROLE` approves the escrow to spend the quote token, then calls
   `fundRedemption(id)` from their wallet or multisig. The admin console provides this flow and
   shows the request's fixed quote and beneficiary compliance status. Funding requires Pending
   status, re-checks that the beneficiary is Allowed and pulls the exact quote amount from the
   treasurer. The request becomes Funded. Funding is irreversible: the escrow has no funded
   cash withdrawal or refund path. Check quote-token eligibility before funding (next section).
3. Claim: anyone can call `claimRedemption(id)` for a Funded request. It pays the exact quote
   amount to the recorded beneficiary and returns the RWA to the Vault as sellable inventory.
   The request becomes Completed. Claim does not re-check beneficiary compliance; the quote
   token's payment restrictions still apply. The investor app offers the action to the
   beneficiary, but the caller can be someone else.
4. Reject: a holder of `REDEMPTION_MANAGER_ROLE` calls `rejectRedemption(id, reasonCode)` with a
   nonzero reason code for a Pending request. The exact RWA returns to the beneficiary, the
   request becomes Rejected and no quote token is paid.
5. Cancel: the beneficiary calls `cancelRedemption(id)` for a Pending request once
   `redemptionTimeout` has elapsed since creation. The exact RWA returns and the request becomes
   Cancelled. This remains available while paused; the investor app offers it after the timeout.

## Before funding: can the beneficiary receive the quote token?

For quote tokens with an allowlist or blacklist, check that the beneficiary can receive
payment. A refusal after funding leaves the request Funded, with its cash and RWA in escrow.
The platform cannot redirect the payout or refund the treasurer. See the
[operator guide, §2a](operator-guide.md#2a-quote-tokens-with-an-allowlist-or-a-blacklist).
This eligibility check is unnecessary for a quote token without those restrictions.

### Fund and claim atomically

For a Safe treasurer, prefer one Transaction Builder batch containing, in order:

1. The quote-token `approve` for the escrow, if the Safe has not already approved enough.
2. `fundRedemption(id)`.
3. `claimRedemption(id)`.

The treasurer can claim because the call is permissionless; payment still goes to the recorded
beneficiary. Use an atomic batch whose calls revert together. If the claim fails, funding rolls
back as well. No other transaction can run between funding and claiming within that batch.
Safe's [MultiSendCallOnly implementation](https://github.com/safe-fndn/safe-smart-account/blob/main/contracts/libraries/MultiSendCallOnly.sol)
reverts the batch when a call fails.

### Simulate a payment

The following call simulates a one-unit transfer without sending tokens:

```bash
cast call <quoteToken> "transfer(address,uint256)(bool)" <beneficiary> 1 \
  --from <treasurerAddress> --rpc-url <rpc>
```

The `--from` address must hold at least one unit of the quote token. A revert or `false` means
this simulated transfer failed. Identify and resolve the cause before funding; if the wallet
is ineligible, reject the request or ask the investor to obtain the quote-token issuer's
approval first. Use the issuer's eligibility check when it provides one.

A successful result tests only that sender, amount and chain state. It does not prove that
the escrow can pay the full quote amount, and restrictions can change before a later claim.
Use the atomic fund-and-claim batch when available.

## Operator responsibilities and SLA

Fund requests under your documented legal and compliance policy. Separate funding authority
(`TREASURER_ROLE`) from rejection authority (`REDEMPTION_MANAGER_ROLE`), and record reason
codes for an audit trail. V1 guarantees neither first-come-first-served funding nor a shared
funding pool.

Watch `pending_redemption_sla` and `funded_claim_failure`; see the
[operator guide, §5](operator-guide.md#5-monitoring-and-alerts). Claim Funded requests promptly.
Anyone can submit the claim, and the beneficiary receives the payment regardless of who calls.

## Edge cases

- Beneficiary blocked or expired while Pending: funding, rejection and cancellation re-check
  compliance and revert until eligibility is restored. A forced transfer cannot recover the
  escrowed RWA because the escrow is prohibited as a seizure source.
- Quote-token refusal while Funded: the claim reverts and the request remains Funded. Retry
  after a temporary problem, such as a quote-token pause or upgrade, has been resolved. A
  beneficiary blacklist requires the quote-token issuer to accept that wallet before payment
  can succeed. Apply the checks above before funding.
- Price changes while Pending: the quote stays fixed at request time. Slippage bounds
  (`maxQuoteAmount` for purchases and `minQuoteOut` for redemption requests) and deadlines limit
  exposure to price changes before a transaction is included.
- Timeout: if the issuer neither funds nor rejects, an Allowed beneficiary can cancel after
  the timeout and recover the exact RWA, even while the project is paused.
- Beneficiary blocked or expired after funding: a registry block or expiry does not stop a
  Funded claim. A project pause does stop it, and the quote token may independently refuse
  payment. The payout remains bound to the recorded beneficiary.

## Re-shelving and burns

A completed redemption is an issuer repurchase: the RWA returns to the Vault and total supply
stays unchanged. Retiring those tokens requires an auditor-signed `BurnAttestation` against
Vault inventory.
