# Maturity runbook

Use this when an instrument matures and holders are to receive par value. The optional
`legal.maturityDate` is informational (see the
[asset profile spec](../spec/asset-profile.md#legal-descriptor)). Contracts have no maturity
state or automatic redemption; operators and holders must use the normal flows.

## Before the date

1. Agree par value per whole token in the quote token, the sales cutoff and redemption window. Notify holders
   through your own channels; the investor app displays `maturityDate` when set.
2. Hold enough quote tokens to fund outstanding holder tokens at par. Unsold Vault inventory
   is excluded.
3. Verify a way to close purchases while keeping redemption available; see step 1 below.

## At maturity

1. **Close sales without pausing redemption.** The supplied strategy has no switch to close sales.
   `setPurchasePrice(2**256 - 1)` does not guarantee that every `buy` reverts: quotes scale by
   token decimals, so fractional purchases may remain payable. Use a separately implemented and
   verified closure mechanism. Project pause also blocks redemption requests, funding and claims.
   Keep sales closed, since completed redemptions return RWA to Vault inventory.
2. **Set redemption price to par.** The pricer calls `setRedemptionPrice(par)` on
   `RedemptionEscrow.strategy()`. Express `par` in quote-token minimal units per whole RWA token.
   The escrow keeps its deploy-time strategy even after `Vault.setStrategy`. Existing requests
   keep their quoted amounts. The redemption manager can reject only Pending requests, with a
   reason code, and ask holders to request again; Funded payouts stay fixed.
3. **Use normal redemption:** request, fund, permissionless claim; see the
   [redemption guide](redemption-ops.md). Fund promptly: an Allowed beneficiary can cancel a
   Pending request after `redemptionTimeout`.
4. **Resolve lapsed compliance.** Ineligible holders cannot request redemption; their Pending
   requests cannot be funded, rejected or cancelled until approval is renewed. See the
   [redemption state machine](../spec/redemption-state-machine.md). Resolve their status before
   the window ends.

## After the redemption window

- Holders who do not redeem keep their tokens. There is no issuer-forced redemption;
  `forcedTransfer` is a seizure, pays no quote and needs its own legal basis.
- Returned RWA remains in Vault inventory and total supply until burned. Keep sales closed.
  After claims return enough tokens to the Vault, retire the amount backed by each record whose
  asset has left custody. Follow the [burn workflow](operator-guide.md#4-day-2-operations) on
  the Assets screen: get each package signed and submit the burn from the admin wallet.
  The Vault must hold each burn's full amount.
- Keep the project, or at least its read-only API, available until all Funded requests are claimed.
