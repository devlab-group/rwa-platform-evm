# Slither static analysis — contracts/

- Tool: `slither-analyzer` 0.11.3 (installed via `pip install slither-analyzer`; not
  preinstalled in this environment).
- Command: `slither . --exclude-dependencies` (run from `contracts/`).
- Scope: `contracts/src/**` (7 contracts + 4 stateless deploy helpers + 8 frozen interfaces).
  `--exclude-dependencies` filters findings whose primary location is inside
  `lib/openzeppelin-contracts/**` — OZ 5.6.1 is treated as a trusted, separately-audited
  dependency, not re-reviewed here. `contracts/script/*.s.sol` (deployment scripts) are not
  picked up by slither's default compilation scope for this repo layout; they were reviewed
  manually instead (see "Deployment-script hardening" below).
- Result: **24 → 16 findings** during this pass, verified unchanged (still 16, no new findings)
  after the later `ComplianceRegistry.setSystemAddresses` change. The 8 that were
  fixed were all missing-zero-address-validation findings (see below); every remaining finding
  was reviewed and is either intentional-by-design or purely informational/stylistic. None
  represent an exploitable vulnerability.

Re-run anytime with:

```sh
cd contracts
pip install slither-analyzer   # if not already installed
slither . --exclude-dependencies
```

## Fixed this pass

### Missing zero-address validation (8 findings → 0)

Several constructors accepted an `address` parameter and stored it without checking it was
non-zero: `RWAToken`'s `compliance_` (constructor) and `controller` (`setSupplyController`);
`RedemptionEscrow`'s `token_`/`quoteToken_`/`vault_`/`strategy_`; `SupplyController`'s
`token_`/`vault_`.

In the actual `RWAFactory.deploy()` flow none of these can ever be zero in practice — every
value comes from either a freshly-`new`'d contract's address (never `address(0)`) or from
`ProjectConfig` fields RWAFactory's own `_validateConfig` already zero-checks. The realistic
risk was therefore a **misuse footgun**: something deploying `RWAToken`/`RedemptionEscrow`/
`SupplyController` directly (bypassing the factory) with a wrong/omitted address would get a
silently-broken contract (e.g. every `isAllowed` call reverting or misbehaving) instead of an
immediate, clear revert at construction time.

**Fix:** added a local `error ZeroAddress();` to each of `RWAToken.sol`, `RedemptionEscrow.sol`,
`SupplyController.sol` (not part of the frozen interfaces — pure constructor-time defense in
depth) and a zero-check on each parameter listed above. `SupplyController` already had a
dedicated `ZeroAddressAuditor` for the auditor param; `token_`/`vault_` now use the new generic
error. Covered by new tests: `RWAToken.t.sol::test_constructor_zeroComplianceReverts`,
`::test_setSupplyController_zeroAddressReverts`; `SupplyController.t.sol::test_constructor_zeroTokenReverts`,
`::test_constructor_zeroVaultReverts`; `RedemptionEscrow.t.sol::test_constructor_zeroAddressReverts`.

(`Vault` already had its own `ZeroAddress()` check in the frozen `IVault` error set and needed
no change. `ComplianceRegistry`'s constructor params are role addresses used only for
`_grantRole`, which is a no-op-safe operation for `address(0)` — not flagged by Slither and left
as-is.)

## Reviewed and accepted (remaining 16 findings)

### `controlled-delegatecall` (1) + `low-level-calls` (1) + `assembly-usage` (1) — all `RWAFactory._delegateDeploy`

Slither flags `target.delegatecall(data)` in `RWAFactory._delegateDeploy` because the function
selector inside `data` isn't statically provable to Slither's heuristic as fixed. In fact it
always is: `target` is one of four `immutable` addresses set once in `RWAFactory`'s constructor
and validated non-zero (`complianceTokenDeployer`, `marketDeployer`,
`supplyControllerDeployer`, `redemptionEscrowDeployer` — see
`src/factory/ComplianceTokenDeployer.sol`'s NatSpec for why these exist: splitting `new` calls
across helper contracts to keep `RWAFactory` under the EIP-170 24,576-byte runtime limit), and
`data` is always built via `abi.encodeCall(SomeDeployer.deploy, (...))` with a
compile-time-fixed selector — never from caller-supplied calldata. No external party can
redirect `target` or the called function. `delegatecall` (not `call`) is required here
specifically so each child contract's constructor sees `msg.sender == address(RWAFactory)`
(needed for `ComplianceRegistry`'s bootstrap `COMPLIANCE_ROLE` grant and `RWAToken`'s
deployer-gated `setSupplyController`). The inline assembly is the standard, `memory-safe`
revert-bubbling pattern (`revert(add(ret, 0x20), mload(ret))`) so a failure inside a deploy
helper surfaces its real revert reason instead of a generic one. Accepted; no change.

### `reentrancy-events` (3) — `SupplyController.mint`/`burn`, `RWAFactory.deploy`

Informational "event emitted after external call(s)" pattern, not a state-changing reentrancy
vulnerability: `RWAToken.controllerMint`/`controllerBurn` only call internal `_mint`/`_burn` (no
external calls or hooks of their own), so there is no way to reenter `SupplyController`
mid-`mint`/`burn` through that path. `RWAFactory.deploy()`'s external calls are all to its own
`immutable` deploy-helper addresses (via `delegatecall`, so no separate contract context to
reenter through) or to the just-created `ComplianceRegistry`/`RWAToken`, neither of which calls
back into `RWAFactory`. `deploy()` isn't reentrancy-guarded because there's no shared, mutable
`RWAFactory` state a reentrant call to `deploy()` could corrupt — each call deploys an
independent project. Accepted; no change.

### `block-timestamp` (6) — attestation `validUntil`, `deadline`, `cancelRedemption` timeout, ComplianceRegistry `validUntil`

Every flagged comparison is an intentional, spec-required expiry/deadline check
(`docs/spec/contracts.md`'s check-ordering tables, `redemption-state-machine.md`'s
`redemptionTimeout`). These operate on day/hour-scale windows; a miner's few-seconds of
`block.timestamp` manipulation latitude is immaterial at that granularity and there is no
alternative (block-number-based deadlines aren't portable across chains with different block
times). Accepted; no change.

### `different-pragma-directives` (informational)

OpenZeppelin's own files pin conservative floor pragmas (`^0.8.20`, `>=0.8.4`, `>=0.6.2`,
`>=0.4.16` for pure interfaces) while our own contracts use `^0.8.24`. This is normal for any
project consuming OZ as a dependency — everything still compiles to a single `0.8.24` solc run
(`foundry.toml`'s `solc_version = "0.8.24"`); the differing pragma *declarations* don't mean
different compiler *versions* actually ran. Accepted; no change.

### `cyclomatic-complexity` (1) — `RWAFactory._validateConfig`

13 independent, unnested `if (...) revert InvalidConfig(...)` checks in a row — a wide, flat
validation function, not deeply-nested/tangled control flow. Every branch is individually
covered by `RWAFactoryTest`'s `test_deploy_zero*Reverts` / `test_deploy_*TooShort/LongReverts`
tests. Splitting it into several smaller functions would move lines around without changing
behavior or improving real reviewability. Left as one function; non-blocking readability nit.

### `naming-convention` (2) — `ISupplyController.MINT_ATTESTATION_TYPEHASH()` / `BURN_ATTESTATION_TYPEHASH()`

Flagged because a `function` name in ALL_CAPS looks like it should be `mixedCase`. These are
part of the `ISupplyController` interface (`docs/spec/contracts.md`,
`shared/eip712/types.md`) matching the on-chain `public constant` getters `SupplyController`
implements for the EIP-712 typehashes — the naming is intentional (constant-style name
for a compile-time-constant value) and the interface is fixed. Accepted; no change possible
without an interface change.

## Deployment-script hardening (manual review, not slither-covered)

`contracts/script/Deploy.s.sol` and `E2EFlow.s.sol` hardcode anvil's well-known, publicly
documented dev-account private keys (`ANVIL_ACCT0_PK`, `ANVIL_ACCT1_PK`) as local-dev
convenience fallbacks for `DEPLOYER_PK`/`INVESTOR_PK`. Manual review found these were
previously used via a bare `vm.envOr(..., ANVIL_ACCT_PK)`, meaning a run against a **real**
chain without explicitly setting the env var would silently deploy using a key anyone can
derive from this repository — handing admin/auditor/treasurer/etc control on the whole project
to anyone who reads it.

**Fix:** `DeployBase._deployerPrivateKey()` / `E2EFlow._investorPrivateKey()` now only allow the
anvil-key fallback when `block.chainid == 31337` (anvil's default chain id); on any other chain
id, an unset `DEPLOYER_PK`/`INVESTOR_PK` reverts with `RefusingAnvilKeyFallbackOnNonAnvilChain`
instead of deploying. Covered by `test/unit/DeployScript.t.sol` (anvil-chain fallback still
works; non-anvil chain without the env var reverts; non-anvil chain with it set explicitly
succeeds).

## ComplianceRegistry system-address pin

Not a Slither finding — surfaced
by the security review (`COMPLIANCE_ROLE` could `Blocked` the Vault or
RedemptionEscrow, freezing settled investor funds in a `Funded` redemption). Remediated with a
one-time `setSystemAddresses(vault, redemptionEscrow)` wiring call plus a guard in
`ComplianceRegistry._setStatus` rejecting any non-`Allowed` status for a pinned system address.
Regression tests in `test/unit/ComplianceRegistry.t.sol` (11 tests) prove: blocking Vault/Escrow
reverts (`setStatus` and batch `setStatuses`), setting them to `Unknown` also reverts (only
`Allowed` is ever accepted), re-`Allowed`-ing succeeds, `setSystemAddresses` is deployer-gated
and one-time, and `buy`/`claimRedemption` all still complete normally after an attempted
(and rejected) block.

## Gas baseline

`contracts/.gas-snapshot` (generated via `forge snapshot`) is the committed per-test gas
baseline — re-run `forge snapshot --diff` before merging any change to `src/` to catch
unintended gas regressions.

## Failure-injection / recovery tests

`test/adversarial/FailureRecovery.t.sol`: underfunded treasurer (fund reverts, request stays
`Pending`, RWA stays escrowed), fee-on-transfer quote token rejected at funding
(`QuoteDeltaMismatch`), and the funded-redemption recovery rehearsal — a quote token blacklists
the beneficiary after funding, `claimRedemption` reverts atomically (status, escrow balances,
and Vault inventory all provably unchanged), then once the blacklist lifts a plain retry of the
same call succeeds.
