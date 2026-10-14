# Contract specification

This file and `contracts/src/interfaces/*.sol` define contract behavior. Interface changes are
frozen-contract changes and require a written, reviewed decision.

Non-upgradeable Solidity `^0.8.24` contracts use OpenZeppelin Contracts 5.x.
ComplianceRegistry, RWAToken, SupplyController, Vault, FixedPriceStrategy and RedemptionEscrow
each inherit `AccessControlEnumerable` and `AccessControlDefaultAdminRules`, providing
enumerable role holders and two-step admin transfer with an on-chain delay. See `roles.md`.

## Project pause

`RWAToken.paused()` is the project emergency flag, controlled by `PAUSER_ROLE` through
`pause()` / `unpause()`.

| While paused | Functions                                                                                                                                                                                                                                                                                  |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Revert       | every RWA transfer (OpenZeppelin `EnforcedPause`), `SupplyController.mint`/`burn`, `Vault.buy`, `withdrawProceeds`, `setStrategy`, `setTreasury`, `RedemptionEscrow.requestRedemption`, `fundRedemption`, `rejectRedemption`, `claimRedemption` (all `ProjectPaused` except the transfers) |
| Keep working | `RedemptionEscrow.cancelRedemption` (through `RWAToken.returnEscrowedRWA`), `RWAToken.setFrozenTokens`, `forcedTransfer`, `SupplyController.setAuditor`, `FixedPriceStrategy` price setters, `ComplianceRegistry.setStatus`/`setStatuses`, role grants and revocations, admin transfers    |

The two token transfer paths available during pause are `returnEscrowedRWA` and `forcedTransfer`,
described below. In an ordinary transfer the compliance and frozen-balance checks run before the
pause check, so an ineligible transfer can revert with its specific error instead of
`EnforcedPause`.

## RWAToken

- OpenZeppelin `ERC20` and `ERC20Pausable`, with decimals fixed at construction.
- Immutable `compliance`; deployer-only, one-time `setSupplyController` and
  `setRedemptionEscrow` wiring (`OnlyTokenDeployer`, `SupplyControllerAlreadySet`,
  `RedemptionEscrowAlreadySet`, `ZeroAddress`). The factory sets both during `deploy`.
- Ordinary `_update` transfers (`from != 0 && to != 0`) require Allowed sender
  (`SenderNotAllowed`) and recipient (`RecipientNotAllowed`), then enough unfrozen balance
  (`ERC7943InsufficientUnfrozenBalance`) when `value <= balanceOf(from)`. Otherwise permitted
  above-balance transfers fail with OpenZeppelin `ERC20InsufficientBalance`.
  Mint and burn skip these eligibility checks.
- `controllerMint` / `controllerBurn` require `supplyController` (`OnlySupplyController`).
- `pause()` / `unpause()` require `PAUSER_ROLE`, initially granted to the project admin.
- `returnEscrowedRWA(to, value)` requires the wired escrow (`OnlyRedemptionEscrow`) and both
  endpoints to be Allowed. It debits the escrow and calls `ERC20._update` directly, bypassing pause.

### ERC-7943 (uRWA)

RWAToken implements the final [ERC-7943](https://eips.ethereum.org/EIPS/eip-7943) fungible
interface in `contracts/src/interfaces/IERC7943.sol`. `supportsInterface(0x3edbb4c4)` returns
true, alongside AccessControl interface IDs. Draft names `isUserAllowed`, `isTransferAllowed`,
`getFrozen`, `setFrozen` and `forceTransfer` have no implementations or aliases.

Frozen amounts are absolute, default to 0 and may exceed a balance:
`unfrozen(a) = balanceOf(a) > frozen[a] ? balanceOf(a) - frozen[a] : 0`.

Read-only queries on the deployed stack do not change state or revert:

- `canSend(a)` / `canReceive(a)` both return `compliance.isAllowed(a)`; the directional API
  keeps them separate.
- `getFrozenTokens(a)` returns the stored amount.
- `canTransfer(from, to, amount)` returns false for pause, disallowed endpoints, or
  `frozen[from] > 0 && amount > unfrozen(from)`. It does not check ERC-20 balance or allowance:
  an above-balance amount can return true when nothing is frozen and fail at execution.
  A fully frozen holder returns false for every positive amount.

Both writes require `DEFAULT_ADMIN_ROLE` and return `true` on success:

- `setFrozenTokens(account, amount)` replaces the amount. It rejects zero `account`
  (`ZeroAddress`) and positive amounts for system addresses (`SystemAddressCannotBeFrozen`).
  Setting a system address to 0 is allowed. Every successful call emits `Frozen(account, amount)`.
- `forcedTransfer(from, to, amount)` rejects zero endpoints (`ZeroAddress`), self-transfer
  (`ForcedTransferToSelf`) and a system-address source (`SystemAddressCannotBeSeized`).
  The destination must pass `canReceive` (`ERC7943CannotReceive`). It bypasses sender eligibility,
  frozen-transfer restrictions and pause, preserves total supply, and lets ERC-20 reject
  above-balance amounts. Consuming frozen tokens reduces the amount by `amount - unfrozen`,
  then emits `Frozen(from, newFrozen)`, `Transfer(from, to, amount)` and
  `ForcedTransfer(from, to, amount)` in that order. Otherwise it emits no `Frozen` event.

Events: `Frozen(address indexed account, uint256 amount)` and
`ForcedTransfer(address indexed from, address indexed to, uint256 amount)`.

#### Integrating with this token

`forcedTransfer` uses `ERC7943CannotReceive(address)`. Ordinary transfers into frozen balance
use `ERC7943InsufficientUnfrozenBalance(address,uint256,uint256)`. They use platform errors
`SenderNotAllowed(address)` and `RecipientNotAllowed(address)` for eligibility; declared standard
errors `ERC7943CannotSend` and `ERC7943CannotTransfer` are never raised. Enforcement also uses
`ZeroAddress()`, `ForcedTransferToSelf(address)`, `SystemAddressCannotBeFrozen(address)` and
`SystemAddressCannotBeSeized(address)`.

`shared/vectors/erc7943-abi.json` pins selectors, including the platform eligibility errors.
The admin console decodes them with `web/src/lib/wallet.ts`'s `describeWalletError` and
`web/src/lib/abis.ts`; the investor app decodes frozen-balance errors with
`investor-web/src/lib/wallet.ts`'s `describeTxError`.

`returnEscrowedRWA` can transfer when pause makes `canTransfer` false, allowing timeout
cancellation. Claim uses it after the escrow's own pause check. Only the wired escrow can call
it, only its own balance is debited, and both endpoints remain compliance-checked. The escrow's
system-address status prevents freezing or seizure of its balance.

## ComplianceRegistry

- `COMPLIANCE_ROLE` controls `setStatus` / `setStatuses`, initially granted to the compliance
  operator and deployer. The factory renounces its grant before `deploy` returns.
- `isAllowed(a)` is `status == Allowed && (validUntil == 0 || validUntil >= block.timestamp)`.
  `getRecord(a)` returns stored status and expiry.
- Batch array-length mismatch raises `ArrayLengthMismatch`. Both setters reject zero accounts
  (`ZeroAddressAccount`) and emit
  `StatusChanged(account, previousStatus, newStatus, previousValidUntil, newValidUntil, caller)`.
- `setSystemAddresses(vault, redemptionEscrow)` is deployer-only and callable once
  (`OnlyRegistryDeployer`, `SystemAddressesAlreadySet`, `ZeroAddressAccount`).
  `isSystemAddress(a)` identifies the pinned addresses. They accept only Allowed with
  `validUntil == 0`; other status or expiry values raise `SystemAddressCannotBeBlocked`.

## SupplyController

`token`, `vault` and `profileDigest` are immutable. Admin-only `setAuditor(newAuditor)` works
while paused, rejects zero (`ZeroAddressAuditor`) and emits `AuditorChanged(previous, new, caller)`.
The constructor rejects zero token or Vault (`ZeroAddress`) and zero auditor
(`ZeroAddressAuditor`). OpenZeppelin `EIP712` uses name `RWA-Supply-Attestation`, version `1`.
`SignatureChecker.isValidSignatureNowCalldata` accepts EOA and ERC-1271 auditors, including a Safe.

`mint(attestation, signature)` runs these checks and effects in order:

1. `!token.paused()`, else `ProjectPaused`
2. `attestation.auditor == auditor`, else `WrongAuditor`
3. `attestation.profileDigest == profileDigest`, else `WrongProfileDigest`
4. `attestation.vault == vault`, else `WrongVault`
5. `block.timestamp <= validUntil`, else `AttestationExpired`
6. `!nonceUsed[nonce]`, else `NonceAlreadyUsed`
7. `!recordKeyUsed[recordKey]`, else `RecordKeyAlreadyUsed`
8. `amount > 0`, else `ZeroAmount`
9. the signature verifies for `auditor` over the EIP-712 digest, else `InvalidSignature`
10. sets `nonceUsed[nonce]` and `recordKeyUsed[recordKey]` (effects before the interaction)
11. `token.controllerMint(vault, amount)`
12. emits `Minted(recordKey, metadataDigest, vault, amount, nonce, auditor)`

`burn(attestation, signature)` uses checks 1 to 6, then unused `operationId`
(`OperationIdAlreadyUsed`), positive amount (`ZeroAmount`), sufficient Vault balance
(`InsufficientVaultInventory`), and valid signature (`InvalidSignature`). It marks nonce and operation ID used, calls
`token.controllerBurn(vault, amount)` and emits
`Burned(operationId, metadataDigest, vault, amount, nonce, auditor)`.

Mint and burn share the nonce namespace; distinct type hashes prevent cross-type replay.
Anyone may submit either: the auditor signature authorizes it.

## FixedPriceStrategy

`quote = Math.mulDiv(tokenAmount, price, 10**tokenDecimals, rounding)`.

- `quotePurchase` / `previewBuy` use `Math.Rounding.Ceil`.
- `quoteRedemption` / `previewRedeem` use `Math.Rounding.Floor`.
- `setPurchasePrice` / `setRedemptionPrice` require `PRICER_ROLE`, including for an admin.
  Zero prices raise `ZeroPrice`, as in construction. They emit
  `PurchasePriceUpdated` / `RedemptionPriceUpdated(previous, new, caller)`.
- Getters: `purchasePricePerWholeToken`, `redemptionPricePerWholeToken`, `tokenDecimals`.

## Vault

`token` and `quoteToken` are immutable. Admin-only `setStrategy` / `setTreasury` reject pause
(`ProjectPaused`) and zero addresses (`ZeroAddress`), emitting
`StrategyChanged` / `TreasuryChanged(previous, new, caller)`; `inventory() == token.balanceOf(vault)`.
The Vault uses `nonReentrant` and `SafeERC20`. Inventory is sold through `buy`; there is no
off-chain distribution, `distribute`, `distributionCap` or `DISTRIBUTOR_ROLE`.

`buy(tokenAmount, maxQuoteAmount, recipient, deadline)` checks, in order: not paused
(`ProjectPaused`), Allowed caller (`CallerNotAllowed`), Allowed recipient (`RecipientNotAllowed`),
`deadline >= block.timestamp` (`DeadlineExpired`), positive amount (`ZeroAmount`), sufficient
inventory (`InsufficientInventory`), and `strategy.quotePurchase(tokenAmount) <= maxQuoteAmount`
(`QuoteAboveMax`). It pulls the quote with `safeTransferFrom`, checks the Vault's exact balance
delta (`QuoteDeltaMismatch`), sends RWA to the recipient and emits
`Purchased(buyer, recipient, tokenAmount, quote, strategy)`.

`withdrawProceeds(amount)` requires `TREASURER_ROLE` and an unpaused project (`ProjectPaused`).
It pays `treasury`, checks its exact quote-token balance delta (`QuoteDeltaMismatch`) and emits
`ProceedsWithdrawn(treasury, amount, caller)`.

## RedemptionEscrow

See `redemption-state-machine.md` for transition guards. `token`, `quoteToken`, `vault`,
`strategy` and `redemptionTimeout` are immutable. Changing `Vault.strategy` leaves redemption
pricing on the original strategy. The escrow uses `nonReentrant` and `SafeERC20`; views are
`getRedemption(id)`, `previewRedeem(rwaAmount)` and `nextId`.

## RWAFactory

The constructor requires four nonzero stateless helper addresses (`ZeroAddressDeployer`).
Fixed-selector `delegatecall`s keep factory runtime code under the EIP-170 limit and make each
child constructor see the factory as `msg.sender`.

Permissionless `deploy(ProjectConfig)` performs these steps in one transaction:

1. Validate config (`InvalidConfig(field)`): nonzero `quoteToken`, `admin`, `auditor`,
   `complianceOperator`, `pricer`, `treasurer`, `redemptionManager` and `treasury`;
   `decimals <= 36`; positive purchase and redemption prices;
   `1 day <= redemptionTimeout <= 365 days`. It does not validate `name`, `symbol`, `projectId`,
   `profileDigest` or `adminTransferDelay`.
2. Deploy ComplianceRegistry and RWAToken, FixedPriceStrategy and Vault, SupplyController, then
   RedemptionEscrow. Constructors grant configured roles, with admin authority on each contract
   and `PAUSER_ROLE` on the token assigned to `config.admin`.
3. Wire `setSupplyController`, `setRedemptionEscrow` and `setSystemAddresses(vault, escrow)`;
   allowlist Vault and escrow without expiry.
4. Renounce bootstrap `COMPLIANCE_ROLE`.
5. Emit `ProjectDeployed(projectId, profileDigest, token, compliance, supplyController, vault,
   redemptionEscrow, strategy, version)` with the first two fields indexed, and return addresses.

The platform derives `projectId` and `profileDigest` from the profile (`asset-profile.md`).
The factory emits both unchanged; the digest also becomes SupplyController's immutable value.
`version()` returns `"rwa-v2"`.
