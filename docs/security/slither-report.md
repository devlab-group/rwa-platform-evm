# Slither static analysis (contracts/)

This report records 16 findings from a local `slither-analyzer` 0.11.3 run, last updated
2026-08-27. That triage classified none as exploitable. It predates `setFrozenTokens`,
`forcedTransfer`, `returnEscrowedRWA` and `setRedemptionEscrow`; no later run covering these
ERC-7943 paths is recorded here. Rerun Slither and update the report before release.

## How Slither runs

- CI runs `crytic/slither-action@v0.4.2` against `contracts` with `--exclude-dependencies` in
  `.github/workflows/ci.yml`. A High finding fails the job.
- Locally:

  ```sh
  cd contracts
  pip install slither-analyzer   # if not already installed
  slither . --exclude-dependencies
  ```

Dependency findings under `lib/openzeppelin-contracts/**` are excluded. OpenZeppelin 5.6.1 is
treated as trusted for this triage; the exclusion does not assess its security. The report covers
`contracts/src/**`: contracts, pricing strategy, four stateless deploy helpers, interfaces and
test token. Deployment-script guards are reviewed separately below.

The workflow also runs Mythril symbolic execution weekly and on demand. That job fails when
`myth analyze` exits nonzero for any contract.

## Accepted findings

### `controlled-delegatecall`, `low-level-calls`, `assembly-usage`: `RWAFactory._delegateDeploy`

Each target is one of four immutable, nonzero helper addresses supplied at construction. Calls
use fixed `abi.encodeCall(SomeDeployer.deploy, (...))` selectors; callers cannot redirect the
target or function. This assumes the factory was deployed with the intended helpers.

Helpers keep factory runtime code below EIP-170's 24,576-byte limit (see
`src/factory/ComplianceTokenDeployer.sol`). `delegatecall` makes child constructors see the
factory as `msg.sender`, preserving bootstrap `COMPLIANCE_ROLE` and deployer-only wiring for
`setSupplyController`, `setRedemptionEscrow` and `setSystemAddresses`.

The `memory-safe` assembly bubbles helper reverts with `revert(add(ret, 0x20), mload(ret))`.
`RWAFactory.sol` suppresses `controlled-delegatecall` with
`// slither-disable-next-line controlled-delegatecall`; the low-level-call and assembly findings
are accepted on the same reviewed path.

### `reentrancy-events`: `SupplyController.mint`/`burn`, `RWAFactory.deploy`

These informational findings mark events emitted after external calls. `controllerMint` and
`controllerBurn` call internal `_mint` / `_burn` without callbacks. Factory deployment uses the
intended helpers and newly created platform contracts, which do not call back into it. The
factory stores no shared mutable project state; each deployment creates an independent stack.

### `block-timestamp`: expiry and deadline checks

Attestation expiry, purchase and redemption-request deadlines, redemption timeout and compliance
expiry intentionally use chain timestamps. Their checks are specified in `docs/spec/contracts.md`
and `docs/spec/redemption-state-machine.md`. Caller-selected deadlines may be close to execution
time; they are not all hours-long windows. Block-number deadlines would depend on each chain's
block interval.

### `different-pragma-directives`

OpenZeppelin uses floor pragmas including `^0.8.20`, `>=0.8.4`, `>=0.6.2` and `>=0.4.16`; platform
contracts use `^0.8.24`. `foundry.toml` pins the compiler to solc 0.8.24 for the whole build.

### `cyclomatic-complexity`: `RWAFactory._validateConfig`

`RWAFactory._validateConfig` uses sequential, unnested `if (...) revert InvalidConfig(...)`
checks. `RWAFactoryTest` covers zero `admin`, `auditor`, `quoteToken`, `complianceOperator`,
`pricer`, `treasurer`, `redemptionManager` and `treasury`; decimals above 36; zero purchase
and redemption prices; and both redemption-timeout bounds. See `test_deploy_zero*Reverts`,
`test_deploy_decimalsTooHighReverts`, `test_deploy_redemptionTimeoutTooShortReverts` and
`...TooLongReverts`. Keeping the validations together makes the configuration boundary clear.

### `naming-convention`: `MINT_ATTESTATION_TYPEHASH()` / `BURN_ATTESTATION_TYPEHASH()`

These `ISupplyController` functions expose `public constant` EIP-712 type hashes from
`SupplyController`. Their constant-style names and interface are frozen (`shared/eip712/types.md`).

## Zero-address validation

The recorded run's eight missing-zero-address findings were fixed. Direct deployment needs these
guards even when factory deployment already supplies created or validated addresses. The guarded
arguments include:

- `RWAToken`: constructor `compliance_`, `setSupplyController` and `setRedemptionEscrow` raise
  `ZeroAddress` (`test_constructor_zeroComplianceReverts`,
  `test_setSupplyController_zeroAddressReverts`, `test_setRedemptionEscrow_zeroAddressReverts`).
- `RedemptionEscrow`: constructor `token_`, `quoteToken_`, `vault_` and `strategy_`
  (`test_constructor_zeroAddressReverts`).
- `SupplyController`: constructor `token_` and `vault_` raise `ZeroAddress`
  (`test_constructor_zeroTokenReverts`, `test_constructor_zeroVaultReverts`);
  the auditor uses `ZeroAddressAuditor`.

`Vault` declares `ZeroAddress` in `IVault` and checks its token, quote token, strategy and
treasury. ComplianceRegistry constructor addresses receive roles: OpenZeppelin's
`AccessControlDefaultAdminRules` rejects a zero admin (`AccessControlInvalidDefaultAdmin`), and
the factory validates the compliance operator. They were not flagged in the recorded run.

## Deployment scripts

`Deploy.s.sol` and `E2EFlow.s.sol` use public Anvil keys `ANVIL_ACCT0_PK` / `ANVIL_ACCT1_PK` when
`DEPLOYER_PK` / `INVESTOR_PK` is unset. `DeployBase._deployerPrivateKey()` and
`E2EFlow._investorPrivateKey()` restrict that fallback to chain ID 31337, otherwise raising
`RefusingAnvilKeyFallbackOnNonAnvilChain`. Deploying with public keys on a real network would
expose their assigned authority.

A zero admin-transfer delay off Anvil raises `RefusingZeroAdminTransferDelayOnNonAnvilChain`
unless `ALLOW_ZERO_ADMIN_TRANSFER_DELAY=true`. `test/unit/DeployScript.t.sol`'s
`test_deployScripts_anvilKeyFallbackAndAdminDelayGuards` checks both guards.

## ComplianceRegistry system-address pin

The deployer's one-time `setSystemAddresses(vault, redemptionEscrow)` protects both addresses
from compliance blocking. Only Allowed with no expiry is accepted; other values raise
`SystemAddressCannotBeBlocked`. This protects purchases and funded claims from a compromised
compliance key and is separate from the recorded Slither findings.

`test/unit/ComplianceRegistry.t.sol` checks both status setters, expiry rejection, re-allowing,
deployer-only and one-time wiring, and purchases and redemptions after rejected blocking attempts.

## Gas baseline

`contracts/.gas-snapshot` is the committed per-test baseline from `forge snapshot`. Run
`forge snapshot --diff` before merging `src/` changes to check for gas regressions.

## Failure-injection and recovery tests

`test/adversarial/FailureRecovery.t.sol` checks:

- Underfunded treasury: funding reverts; the request stays Pending with RWA escrowed.
- Fee-on-transfer quote token: funding raises `QuoteDeltaMismatch`.
- Beneficiary blacklisted after funding: claim reverts without changing status, escrow balances
  or Vault inventory, then succeeds after the blacklist lifts.
- Transfer fee enabled after funding: claim reverts, then succeeds after the fee is disabled.
