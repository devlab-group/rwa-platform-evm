# Contract specification

The Solidity interfaces in `contracts/src/interfaces/*.sol`, together with this file, describe
how the contracts behave. Implementations follow them; changing an interface means writing an
ADR for it first.

Solidity `^0.8.24`, OpenZeppelin Contracts 5.x. All contracts non-upgradeable.

## Project pause

`RWAToken.paused()` is the single project-wide emergency flag (from `ERC20Pausable`
+ a `PAUSER_ROLE`-gated `pause()/unpause()`). `SupplyController`, `Vault`, and
`RedemptionEscrow` MUST read `token.paused()` and revert state-changing calls while paused.

## RWAToken

- OZ `ERC20` + `ERC20Pausable`, fixed decimals from constructor.
- `_update(from,to,value)`: if `from!=0 && to!=0` require `compliance.isAllowed(from)`
  and `compliance.isAllowed(to)`. Mint/burn (`from==0`/`to==0`) skip the check.
- `controllerMint`/`controllerBurn`: only `supplyController` (immutable).
- `pause()`/`unpause()`: `PAUSER_ROLE` (use OZ AccessControl on the token, or pass
  through a shared roles source — implementer choice, but PAUSER must be the factory-set holder).

## ComplianceRegistry

- OZ `AccessControl`, `COMPLIANCE_ROLE` gates `setStatus`/`setStatuses`.
- `isAllowed`: status\==Allowed && (validUntil\==0 || validUntil>=block.timestamp).
- `setStatuses`: revert `ArrayLengthMismatch` if lengths differ; each element emits `StatusChanged`.
- Emits previous + new status/expiry + caller.

## SupplyController

Immutable: `token`, `vault`, `profileDigest`. Mutable via `DEFAULT_ADMIN_ROLE`: `auditor`.
Uses OZ `EIP712` + `SignatureChecker.isValidSignatureNow` (EOA + ERC-1271).

`mint(attestation, signature)` checks IN ORDER:
1. `!token.paused()` else `ProjectPaused`
2. `attestation.auditor == auditor` else `WrongAuditor`
3. `attestation.profileDigest == profileDigest` else `WrongProfileDigest`
4. `attestation.vault == vault` else `WrongVault`
5. `block.timestamp <= validUntil` else `AttestationExpired`
6. `!nonceUsed[nonce]` else `NonceAlreadyUsed`
7. `!recordKeyUsed[recordKey]` else `RecordKeyAlreadyUsed`
8. `amount > 0` else `ZeroAmount`
9. `SignatureChecker.isValidSignatureNow(auditor, digest, signature)` else `InvalidSignature`
10. set `nonceUsed[nonce]=true`, `recordKeyUsed[recordKey]=true` (effects before interaction)
11. `token.controllerMint(vault, amount)`
12. emit `Minted`

`burn(attestation, signature)`: same 1-6, 8, 9; replace record-key check with
`!operationIdUsed[operationId]`; additionally require `amount <= token.balanceOf(vault)`
else `InsufficientVaultInventory`; set nonce+operationId used; `token.controllerBurn(vault, amount)`; emit `Burned`.

Nonce namespace is shared across mint and burn. Distinct type hashes prevent cross-type replay.
`mint`/`burn` are permissionless to call.

## FixedPriceStrategy

`quote = Math.mulDiv(tokenAmount, price, 10**tokenDecimals, rounding)`.
- `quotePurchase` / `previewBuy`: `Math.Rounding.Ceil`.
- `quoteRedemption` / `previewRedeem`: `Math.Rounding.Floor`.
- `setPurchasePrice`/`setRedemptionPrice`: `PRICER_ROLE`; revert `ZeroPrice` on 0.

## Vault

Immutable: `token`, `quoteToken`. Mutable: `strategy`, `treasury` (admin). Holds RWA inventory
(`inventory()==token.balanceOf(this)`). `nonReentrant` + `SafeERC20`. `buy` is the only inbound
token flow (ADR-007 removed off-chain payment distribution — `distribute`, `distributionCap`,
`DISTRIBUTOR_ROLE` no longer exist).

`buy(tokenAmount, maxQuoteAmount, recipient, deadline)`:
- `!paused`; caller & recipient `isAllowed`; `deadline>=block.timestamp`;
  `tokenAmount>0 && <=inventory`; `quote=strategy.quotePurchase(tokenAmount)`;
  `quote<=maxQuoteAmount`; pull quote via `safeTransferFrom` and assert exact balance delta;
  then `token.transfer(recipient, tokenAmount)`; emit `Purchased`.

`withdrawProceeds(amount)`: `TREASURER_ROLE`; transfer quoteToken to `treasury`; emit `ProceedsWithdrawn`.
`setStrategy`/`setTreasury` admin.

## RedemptionEscrow

See `redemption-state-machine.md`. Immutable: `token`, `quoteToken`, `vault`, `strategy`,
`redemptionTimeout`. `nonReentrant`, `SafeERC20`, exact balance-delta checks on every transfer.

## RWAFactory

`deploy(ProjectConfig)` deploys the full stack in one tx, wires addresses, registers `vault`
and `redemptionEscrow` as `Allowed` in the registry, grants final roles, revokes all temporary
factory permissions, and emits a single `ProjectDeployed`. Validates config (nonzero addresses,
timeout within bounds, nonzero prices, decimals<=36) else `InvalidConfig(field)`.
`version()` returns a constant like `"rwa-v2"`.
