# Redemption operations guide

V1 redemption pays the investor in the configured ERC-20 quote token. It does not deliver the
physical underlying. State machine: `None → Pending → {Funded → Completed | Rejected | Cancelled}`
(see `../spec/redemption-state-machine.md`).

## Lifecycle

1. **Request** (investor): approves `RedemptionEscrow` and calls
   `requestRedemption(rwaAmount, minQuoteOut, deadline)`. The contract snapshots
   `quoteAmount = strategy.quoteRedemption(rwaAmount)`, checks slippage/deadline, and escrows the
   exact RWA. Status → Pending. The UI must never call a pending request a guaranteed payment.
2. **Fund** (TREASURER_ROLE): the admin queue shows Pending requests with the snapshotted quote and
   beneficiary compliance. The server produces `fund-calldata`; the treasury multisig submits it.
   Funding re-checks the beneficiary is Allowed and transfers the exact quote in. Status → Funded.
   **Funding is irreversible** — there is no path to withdraw funded quote.
3. **Claim** (anyone, permissionless): `claim-calldata`; pays the recorded beneficiary and returns
   the RWA to the Vault as sellable inventory. Status → Completed. Claim does NOT re-check the
   whitelist (compliance is enforced at funding).
4. **Reject** (REDEMPTION_MANAGER_ROLE, only Pending): `reject-calldata` with a nonzero reason code;
   returns exact RWA to the beneficiary. No quote paid.
5. **Cancel** (beneficiary only, only Pending, after `redemptionTimeout`): returns exact RWA.

## Operator responsibilities & SLA

- Decide funding per your documented legal/compliance policy. Role separation (treasurer funds,
  redemption-manager rejects) and reason codes give an auditable trail; V1 makes no FIFO/pooled
  guarantee.
- Monitor the pending-redemption SLA alert (age threshold) and the funded-but-unclaimed alert.
  A funded request should be claimed promptly; anyone can claim it for the beneficiary.

## Edge cases

- **Beneficiary blocked after request**: funding re-checks compliance and reverts if blocked;
  reject/cancel also revert if the beneficiary can't receive RWA. The request stays Pending until
  compliance is restored. V1 has no force-transfer bypass.
- **Quote-token blacklist/failure on a funded claim**: the claim reverts but the request stays
  Funded and remains claimable — retry once the quote token recovers.
- **Price change while a request is pending**: the request's quote was snapshotted at request time;
  buyer max-quote / redemption min-quote + deadlines bound front-running.
- **Timeout**: if the issuer neither funds nor rejects, the beneficiary may cancel after the timeout
  and recover the exact RWA (if still Allowed to receive it).

## Re-shelving & burns

A completed redemption returns RWA to the Vault (issuer repurchase); total supply is unchanged.
Permanently retiring supply requires an auditor-signed `BurnAttestation` against Vault inventory.
