# Contract specification

The Solidity interfaces in `contracts/src/interfaces/*.sol`, together with this file, describe
how the contracts behave. Implementations follow them; changing an interface is a
frozen-contract change and needs a written, reviewed decision first.

Solidity `^0.8.24`, OpenZeppelin Contracts 5.x. All contracts non-upgradeable.

## Project pause

`RWAToken.paused()` is the single project-wide emergency flag (from `ERC20Pausable`
+ a `PAUSER_ROLE`-gated `pause()/unpause()`). `SupplyController`, `Vault`, and
`RedemptionEscrow` MUST read `token.paused()` and revert state-changing calls while paused.
Two token paths are deliberately exempt so an emergency pause cannot trap them:
`RWAToken.returnEscrowedRWA` (redemption cancel/claim recovery) and `RWAToken.forcedTransfer`
(ERC-7943 enforcement). Both are documented under RWAToken below.

## RWAToken

- OZ `ERC20` + `ERC20Pausable`, fixed decimals from constructor.
- `_update(from,to,value)`: if `from!=0 && to!=0` require `compliance.isAllowed(from)`
  and `compliance.isAllowed(to)`, then require `value <= unfrozen(from)` else
  `ERC7943InsufficientUnfrozenBalance`. Mint/burn (`from==0`/`to==0`) skip both checks.
- `controllerMint`/`controllerBurn`: only `supplyController` (immutable).
- `pause()`/`unpause()`: `PAUSER_ROLE` (use OZ AccessControl on the token, or pass
  through a shared roles source - implementer choice, but PAUSER must be the factory-set holder).
- `returnEscrowedRWA(to,value)`: only the wired `redemptionEscrow`; both ends still
  `isAllowed`; calls `ERC20._update` directly so it works while paused. The escrow is a
  system address and therefore never frozen.

### ERC-7943 (uRWA)

RWAToken implements the final ERC-7943 fungible interface (`contracts/src/interfaces/IERC7943.sol`).
`supportsInterface(0x3edbb4c4)` is true, alongside the AccessControl interface IDs. The obsolete
draft names from the ERC's draft period (`isUserAllowed`, `isTransferAllowed`, `getFrozen`,
`setFrozen`, `forceTransfer`) are not implemented and not aliased.

Frozen amounts are absolute per holder, default 0, and may exceed the holder's balance, so every
consumer computes `unfrozen(a) = balanceOf(a) > frozen[a] ? balanceOf(a) - frozen[a] : 0`.

Reads (side-effect-free, must not revert):
- `canSend(a)` / `canReceive(a)`: both `compliance.isAllowed(a)`. Kept separate for the
  standard's directional API.
- `getFrozenTokens(a)`: the stored absolute amount.
- `canTransfer(from,to,amount)`: false if `paused()`, `!canSend(from)`, `!canReceive(to)`, or
  `amount <= balanceOf(from) && amount > unfrozen(from)`. It does NOT report ERC-20 balance or
  allowance insufficiency: an over-balance amount returns true here and the ERC-20 transfer
  rejects it at execution. The frozen clause mirrors exactly what `_update` enforces.

Writes, both `DEFAULT_ADMIN_ROLE`:
- `setFrozenTokens(account,amount)`: overwrite (not a delta); rejects `account==0`; rejects
  `amount>0` when `compliance.isSystemAddress(account)`; `amount==0` on a system address stays
  allowed; emits `Frozen(account,amount)` on every successful call.
- `forcedTransfer(from,to,amount)`: rejects `from==0`, `to==0`, `from==to`; requires
  `canReceive(to)`; deliberately bypasses `canSend(from)`, `canTransfer`, and project pause;
  never changes total supply; over-balance reverts through the ERC-20 balance path. If
  `amount` reaches into frozen tokens, reduce first (`newFrozen = frozen - max(amount -
  unfrozen, 0)`) and emit before the transfer:
  `Frozen(from,newFrozen)`, `Transfer(from,to,amount)`, `ForcedTransfer(from,to,amount)`.
  With nothing frozen consumed, the `Frozen` event is omitted.

Events: `Frozen(address indexed account, uint256 amount)`,
`ForcedTransfer(address indexed from, address indexed to, uint256 amount)`.

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
token flow: there is no off-chain payment distribution (`distribute`, `distributionCap`,
`DISTRIBUTOR_ROLE` do not exist).

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
