# Slither static analysis (contracts/)

On 2026-10-14, `slither-analyzer` 0.11.3 analyzed 61 contracts with 100 detectors using
`slither . --exclude-dependencies` from `contracts/`. Coverage includes `setFrozenTokens`,
`forcedTransfer`, `returnEscrowedRWA` and `setRedemptionEscrow`.

The initial run reported 24 findings. After the three `TestToken` fixes, a rerun reported
21, all Low or Informational, meeting CI's High-severity threshold. The table includes the
fixed findings; the remaining findings are accepted for the reasons below.

| Detector | Severity | Results | Outcome |
| --- | --- | --- | --- |
| `shadowing-local` | Low | 2 | fixed (`TestToken`) |
| `reentrancy-events` | Low | 3 | accepted |
| `timestamp` | Low | 6 | accepted |
| `assembly` | Informational | 1 | accepted |
| `pragma` | Informational | 1 | accepted |
| `cyclomatic-complexity` | Informational | 1 | accepted |
| `dead-code` | Informational | 6 | false positive |
| `low-level-calls` | Informational | 1 | accepted |
| `naming-convention` | Informational | 2 | accepted |
| `immutable-states` | Optimization | 1 | fixed (`TestToken`) |

Severities follow Slither's detector classifications. The source suppresses
`controlled-delegatecall` (High); its rationale is below.

## How Slither runs

- CI runs `crytic/slither-action@v0.4.2` against `contracts` with `--exclude-dependencies` in
  `.github/workflows/ci.yml`. A High finding fails the job.
- Locally:

  ```sh
  cd contracts
  pip install slither-analyzer   # if not already installed
  slither . --exclude-dependencies
  ```

The build excludes tests and scripts (`forge build --skip ./test/** ./script/**`). Analysis
covers `contracts/src/**`: contracts, pricing strategy, four stateless deploy helpers,
interfaces and `src/test/TestToken.sol`. Findings in `lib/openzeppelin-contracts/**` are
excluded, treating OpenZeppelin 5.6.1 as trusted without assessing its security.
Deployment-script guards are reviewed below.

The workflow also runs Mythril symbolic execution weekly and on demand. That job fails when
`myth analyze` exits nonzero for any contract.

## Fixed findings

### `shadowing-local` and `immutable-states`: `TestToken`

`script/DeployTestToken.s.sol` deploys `src/test/TestToken.sol` as a quote token for local and
test use; it is not intended for production. Constructor parameters `name` and `symbol`
shadowed `ERC20.name()` and `symbol()`, so they were renamed `name_` and `symbol_`.
`_decimals` is now `immutable` because it is assigned only at construction. The pragma is
`^0.8.24`, matching the other platform contracts.

## Accepted findings

### `dead-code`: `_setRoleAdmin` in six contracts

ComplianceRegistry, FixedPriceStrategy, RWAToken, RedemptionEscrow, SupplyController and Vault
each forward `_setRoleAdmin(bytes32,bytes32)` to `super`. Slither reports these unused overrides,
but Solidity requires them: `AccessControlEnumerable` inherits the `AccessControl` implementation,
while `AccessControlDefaultAdminRules` overrides it. Removing the explicit resolution produces
solc error 6480 ("Derived contract must override function
`_setRoleAdmin`"). The stubs add no behavior; `super` reaches `AccessControlDefaultAdminRules`,
preserving its refusal to change the admin of `DEFAULT_ADMIN_ROLE`.

### `controlled-delegatecall`, `low-level-calls`, `assembly`: `RWAFactory._delegateDeploy`

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

These Low findings flag events emitted after external calls. `controllerMint` and
`controllerBurn` use internal `_mint` / `_burn` without callbacks. Factory deployment uses the
intended helpers and newly created platform contracts, which do not call back into it. The
factory stores no shared mutable project state; each deployment creates an independent stack.

### `timestamp`: expiry and deadline checks

Attestation expiry, purchase and redemption-request deadlines, redemption timeout and compliance
expiry intentionally use chain timestamps. Their checks are specified in `docs/spec/contracts.md`
and `docs/spec/redemption-state-machine.md`. Caller-selected deadlines can be close to execution
time. Block-number deadlines would depend on each chain's block interval.

### `pragma`

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

The run reports no missing-zero-address findings. These guards also protect direct deployments,
where the factory has not supplied created or validated addresses:

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
the factory validates the compliance operator. Slither does not flag them.

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
compliance key and is not a Slither finding.

`test/unit/ComplianceRegistry.t.sol` checks both status setters, expiry rejection, re-allowing,
deployer-only and one-time wiring, and purchases and redemptions after rejected blocking attempts.

## Gas baseline

`contracts/.gas-snapshot` is the committed per-test baseline from `forge snapshot`.
`foundry.toml` fixes the fuzz seed to help reproduce test inputs; `forge snapshot --check`
fails when results differ from the baseline. Compare under the same toolchain and settings,
and run `forge snapshot --diff` before merging `src/` changes to review gas regressions.

## Failure-injection and recovery tests

`test/adversarial/FailureRecovery.t.sol` checks:

- Underfunded treasury: funding reverts; the request stays Pending with RWA escrowed.
- Fee-on-transfer quote token: funding raises `QuoteDeltaMismatch`.
- Beneficiary blacklisted after funding: claim reverts without changing status, escrow balances
  or Vault inventory, then succeeds after the blacklist lifts.
- Transfer fee enabled after funding: claim reverts, then succeeds after the fee is disabled.
