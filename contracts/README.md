# contracts: RWA tokenization stack (Foundry)

Solidity `^0.8.24`, OpenZeppelin Contracts 5.6.1, all contracts non-upgradeable. One
`RWAFactory` deployment per platform instance; one `RWAFactory.deploy(ProjectConfig)` call per
tokenized asset ("project").

[`docs/spec/`](../docs/spec/) defines check order and contract behavior in `contracts.md`,
roles and mutability in `roles.md`, and escrow guards in `redemption-state-machine.md`.
Contracts implement the stable interfaces in `src/interfaces/*.sol`. Update the specification
before changing an interface.

## Contract set (`src/`)

| Contract | Role |
| --- | --- |
| `ComplianceRegistry.sol` | Wallet allowlist with optional KYC expiry. Pins `Vault` and `RedemptionEscrow` as system addresses that cannot be blocked (see below). |
| `RWAToken.sol` | ERC-20 with `ERC20Pausable` and the ERC-7943 fungible interface. Ordinary transfers need both parties Allowed and respect frozen amounts; only the SupplyController mints or burns; the deployer wires `supplyController` and `redemptionEscrow` once. |
| `SupplyController.sol` | Validates EIP-712 auditor attestations (OpenZeppelin `EIP712` and `SignatureChecker`, so EOA and ERC-1271 auditors) and mints to or burns from the Vault through `RWAToken`. |
| `Vault.sol` | Holds RWA inventory; `buy` sells it for the quote token and `withdrawProceeds` pays the treasury. |
| `pricing/FixedPriceStrategy.sol` | Purchase and redemption prices that `PRICER_ROLE` holders set; `Math.mulDiv` rounds `Ceil` for purchases and `Floor` for redemptions. |
| `RedemptionEscrow.sol` | Cash redemption state machine: request, fund, then permissionless claim, plus reject and cancel. |
| `RWAFactory.sol` | One-transaction deploy and wiring for a whole project; a thin orchestrator over the four helpers below. |
| `factory/ComplianceTokenDeployer.sol` | Stateless helper that creates `ComplianceRegistry` and `RWAToken`. |
| `factory/MarketDeployer.sol` | Stateless helper that creates `FixedPriceStrategy` and `Vault`. |
| `factory/SupplyControllerDeployer.sol` | Stateless helper that creates `SupplyController`. |
| `factory/RedemptionEscrowDeployer.sol` | Stateless helper that creates `RedemptionEscrow`. |

`src/test/TestToken.sol` is a mintable ERC-20 that `script/DeployTestToken.s.sol` deploys.

### Why the deploy helpers exist (EIP-170)

Calling `new` in a runtime function embeds the child's creation code in its caller.
`RWAFactory.deploy()` remains callable for each project, so its child creation code is split
across helpers to stay below EIP-170's 24,576-byte runtime limit. Chains enforcing EIP-170
reject larger deployments. `test/unit/ContractSizeLimit.t.sol` asserts `.code.length <= 24576`
for the factory, four helpers and six project contracts because `forge test` does not enforce
the limit by default.

The `new` calls therefore live in four small stateless deployer contracts that `RWAFactory`
invokes through `delegatecall`, not `call`. `delegatecall` keeps `address(this)` as
`RWAFactory`, so each child's constructor sees `msg.sender == address(RWAFactory)`.
`ComplianceRegistry`'s bootstrap `COMPLIANCE_ROLE` grant and the one-time deployer gates on
`RWAToken` and `ComplianceRegistry` depend on that.

`RWAFactory`'s constructor takes the four helper addresses, so deploy them first and then call
`new RWAFactory(complianceTokenDeployer, marketDeployer, supplyControllerDeployer,
redemptionEscrowDeployer)`. `forge build --sizes` prints the current sizes.

## Deploy order & wiring (`RWAFactory.deploy`)

1. Validate the config (`InvalidConfig(field)`; see `docs/spec/contracts.md` for the checks).
2. `ComplianceTokenDeployer.deploy()` creates `ComplianceRegistry` and `RWAToken`. The registry's
   constructor grants `COMPLIANCE_ROLE` to the configured `complianceOperator` and, as a
   bootstrap right, to `RWAFactory`. The token is created with `supplyController` and
   `redemptionEscrow` unset.
3. `MarketDeployer.deploy()` creates `FixedPriceStrategy` and `Vault`.
4. `SupplyControllerDeployer.deploy()` creates `SupplyController`, which needs the `Vault` address.
5. `RedemptionEscrowDeployer.deploy()` creates `RedemptionEscrow`, which needs the token, `Vault`
   and `FixedPriceStrategy` addresses.
6. `token.setSupplyController(supplyController)` and `token.setRedemptionEscrow(escrow)`. Both
   are one-time, deployer-only setters. They break the construction cycle: `SupplyController`
   needs the `Vault` address, the `Vault` needs the token address, and the token needs the
   `SupplyController` address (the escrow likewise needs the token). Each setter is locked after
   its first call, so the values behave as immutable even though they are not the Solidity
   `immutable` keyword.
7. `compliance.setSystemAddresses(vault, escrow)` pins both as system addresses (see below).
8. `compliance.setStatus(vault, Allowed, 0)` and the same for `escrow`. Step 7 only restricts
   what `setStatus` can set later; it does not allow the addresses by itself.
9. `compliance.renounceRole(COMPLIANCE_ROLE, address(this))` drops the factory's bootstrap right.
   `renounceRole` needs no special permission, unlike `revokeRole`.
10. `emit ProjectDeployed(...)` and return the `Deployment` struct.

**No residual roles.** After `deploy()` returns, `RWAFactory` holds no role on any contract. It
never holds `DEFAULT_ADMIN_ROLE`, and every operational role goes directly to the address named
in `ProjectConfig`. `test/invariant/RedemptionInvariant.t.sol`
(`invariant_factoryHasNoResidualComplianceRole`) checks this across fuzzed call sequences, and
`ComplianceRegistryTest::test_factoryBootstrapRoleWasRenounced`,
`RWAFactoryTest::test_deploy_factoryRetainsNoPrivileges` and
`RoleEnumerationTest::test_factory_holdsNoBootstrapRoleOnAnyChildPostDeploy` check it directly.

## Deploying the factory (bootstrap, `script/DeployFactory.s.sol`)

The reusable `RWAFactory` must exist on-chain before any project can be deployed. Deploy it once
per chain; every project is then created by calling `factory.deploy(config)`, which the admin
console does from the admin's wallet. The factory is permissionless deploy machinery: its
constructor takes no admin or owner and grants no roles. `script/Deploy.s.sol` deploys its own
factory and one project in a single run, for local and staging use.

```sh
forge script script/DeployFactory.s.sol --rpc-url <your-rpc-url> --broadcast -vv
```

**Input.** One env var, `DEPLOYER_PK`, the private key to broadcast from. The account needs
enough gas for five contract creations: the four stateless helpers and the `RWAFactory` that
uses them. On anvil (`chainid 31337`) `DEPLOYER_PK` defaults to the well-known anvil account 0.
On any other chain it must be set, or the run reverts with
`RefusingAnvilKeyFallbackOnNonAnvilChain` instead of broadcasting from a key printed in anvil's
startup banner.

**Output.** The script logs the two values the server needs in its config `contract` block:

| Logged value | Server config key | Meaning |
|---|---|---|
| factory address | `contract.factory_address` | the address the server bootstraps from; deployed-project addresses are then read from the chain and the database, not from config |
| deployment block (`block.number`) | `contract.start_block` | the block the indexer starts scanning from |

## ComplianceRegistry system-address pin

Without the pin, `COMPLIANCE_ROLE` could set `Vault` or `RedemptionEscrow` to `Blocked` like any
user wallet. That would break `buy`, `rejectRedemption` and `claimRedemption`, which all move RWA
through `RWAToken`'s compliance checks, and a funded redemption's quote would stay in the escrow
until the address was allowed again. A single mistaken batch entry or a compromised compliance
key would then disable the deployment.

`ComplianceRegistry.setSystemAddresses(vault, redemptionEscrow)` is one-time and deployer-only
(the same pattern as `RWAToken.setSupplyController`) and records the two addresses. After that,
`setStatus` and `setStatuses` accept only `Allowed` with `validUntil == 0` for them and revert
with `SystemAddressCannotBeBlocked` otherwise. The expiry is pinned too because an `Allowed`
record with a nonzero `validUntil` stops passing `isAllowed` once it expires. The pin applies for
the life of the deployment. `test/unit/ComplianceRegistry.t.sol` includes a test that completes
`buy`, request, fund and claim right after a rejected block attempt, which shows the normal
flows keep working.

## Roles (`docs/spec/roles.md`)

| Role | Powers |
| --- | --- |
| `DEFAULT_ADMIN_ROLE` (every contract) | Grant and revoke that contract's roles; `SupplyController.setAuditor`; `Vault.setStrategy` and `setTreasury`; `RWAToken.setFrozenTokens` and `forcedTransfer`. |
| `PAUSER_ROLE` (`RWAToken`) | `pause()` and `unpause()`, the single project-wide emergency flag. |
| `COMPLIANCE_ROLE` (`ComplianceRegistry`) | `setStatus` and `setStatuses`. |
| `PRICER_ROLE` (`FixedPriceStrategy`) | `setPurchasePrice` and `setRedemptionPrice`. |
| `TREASURER_ROLE` (`Vault`, `RedemptionEscrow`) | `Vault.withdrawProceeds` and `RedemptionEscrow.fundRedemption`. |
| `REDEMPTION_MANAGER_ROLE` (`RedemptionEscrow`) | `rejectRedemption`. |

The auditor is an address stored on `SupplyController` (replaced through the admin-only
`setAuditor`), not a role. `mint` and `burn` are callable by anyone who holds a valid auditor
signature. Each contract has its own `AccessControlDefaultAdminRules` instance and there is no
shared roles hub. The factory grants every role directly to the address in `ProjectConfig`.

## EIP-712 attestation flow (`SupplyController`)

Domain: `name: "RWA-Supply-Attestation"`, `version: "1"`, the current `chainId`, and
`verifyingContract` set to the `SupplyController` instance. The canonical type strings and field
order are in [`shared/eip712/types.md`](../shared/eip712/types.md); the typehash and digest
vectors are in `shared/vectors/typehashes.json` and `shared/vectors/mint-eip712.json`.

`mint(attestation, signature)` checks, in order: the project is not paused, the attestation
auditor matches the stored auditor, the profile digest matches, the vault matches, the
attestation has not expired, the nonce is unused, the record key is unused, the amount is
nonzero, and `SignatureChecker.isValidSignatureNowCalldata` accepts the signature (EOA or
ERC-1271). It then marks the nonce and record key used, calls `token.controllerMint` and emits
`Minted`. `docs/spec/contracts.md` lists the checks and their errors. `burn` has the same shape
with `operationId` in place of `recordKey` and an added check that the amount does not exceed
`token.balanceOf(vault)`.

Mint and burn share one nonce namespace, so a nonce used by either call reverts the other.
Distinct type hashes keep a mint signature from validating as a burn attestation and the
reverse.

`test/unit/Vectors.t.sol` reproduces the golden vector byte for byte. It deploys
`SupplyController` at the vector's `verifyingContract` address (`vm.setNonce` and `vm.prank`
from anvil account 0 at nonce 0), then asserts the typehashes, domain separator, struct hash and
digest, and that `ECDSA.recover(digest, signature)` returns the auditor. The same file pins the
ERC-7943 selectors against `shared/vectors/erc7943-abi.json` and the rounding cases against
`shared/vectors/arithmetic.json`. `test/unit/SafeAuditorVectors.t.sol` reproduces the hashes and
owner signatures of `shared/vectors/safe-auditor.json` without deploying a Safe.

## Pricing & rounding (`FixedPriceStrategy`)

`quote = Math.mulDiv(tokenAmount, price, 10**tokenDecimals, rounding)`, where `price` is
quote-token smallest units per whole RWA token. Purchases round `Ceil` so the Vault is never
underpaid; redemptions round `Floor` so the Vault never overpays.
`RedemptionEscrow.requestRedemption` fixes `quoteAmount` at request time with
`strategy.quoteRedemption(rwaAmount)`, so later price changes do not affect a pending request.
`ArithmeticVectorsTest` in `test/unit/Vectors.t.sol` checks the cases in
`shared/vectors/arithmetic.json` against both the deployed contract and a direct `Math.mulDiv`
call.

## Redemption state machine (`RedemptionEscrow`)

A request starts `Pending`. From there the treasurer funds it (`Funded`, then `Completed` on
claim), the redemption manager rejects it (`Rejected`), or the beneficiary cancels it after the
timeout (`Cancelled`). The full guard table is in
[`docs/spec/redemption-state-machine.md`](../docs/spec/redemption-state-machine.md).
`test/invariant/RedemptionInvariant.t.sol` covers the accounting
(`invariant_escrowRwaBackedByOpenRequests`, `invariant_escrowQuoteBackedByFundedRequests`).
Notable rules:

- A request's RWA leaves escrow exactly once (claim, reject or cancel). A funded request's quote
  is paid once, only at claim, and only to the recorded beneficiary. No role can withdraw funded
  but unclaimed quote.
- `claimRedemption` is permissionless and does not recheck the beneficiary's compliance status;
  compliance is checked when the request is created and again at funding. If the beneficiary is
  blocked after funding, claim still completes, while reject and cancel revert because the
  request is no longer Pending.
- `cancelRedemption` has no `token.paused()` check, and it works during an emergency pause. Its
  RWA leg goes through `RWAToken.returnEscrowedRWA`, which moves the escrow's own balance to a
  compliant beneficiary whether or not the token is paused. `claimRedemption` returns the RWA
  to the Vault through the same function, but checks the pause flag first. The other transitions
  revert while paused. `test_cancelRedemption_succeedsWhilePaused` covers cancel under pause.
- Pull-in legs assert an exact balance delta: `requestRedemption` for the RWA, `fundRedemption`
  for the quote. A fee-on-transfer or short-transferring token reverts the call
  (`RwaDeltaMismatch`, `QuoteDeltaMismatch`) instead of under-crediting the escrow. `claim` and
  `Vault.withdrawProceeds` apply the same delta check to the quote paid out.

## Build & test

```sh
cd contracts
forge build              # compile; ABIs land under out/
forge build --sizes      # confirm every contract is under the EIP-170 24,576 B limit
forge test               # all suites below
forge test -vv           # with traces
forge fmt --check        # formatting check (forge fmt to apply)
forge snapshot           # regenerate .gas-snapshot after a src/ change
forge snapshot --diff    # compare against the committed baseline before merging
bash script/e2e.sh       # live anvil broadcast of the full flow (see below)
```

### Test layout (`forge test`)

- `test/unit/` has one file per contract (`ComplianceRegistry`, `RWAToken`, `SupplyController`,
  `Vault`, `FixedPriceStrategy`, `RedemptionEscrow`, `RWAFactory`), plus `RoleEnumeration.t.sol`
  (`AccessControlEnumerable` on every contract), `Vectors.t.sol` and `SafeAuditorVectors.t.sol`
  (golden vectors), `ContractSizeLimit.t.sol` (EIP-170 guard), and `DeployScript.t.sol` and
  `DeployFactoryScript.t.sol` (the deploy scripts). Fuzz tests live inline in the unit files
  (`testFuzz_*`).
- `test/integration/FullFlow.t.sol` runs deploy, allow, mint (locally signed attestation), buy,
  requestRedemption, fund, claim and an auditor burn of the returned Vault inventory. It also
  covers a timeout and cancel branch, the ERC-7943 freeze and seizure lifecycle, and the rule
  that enforcement cannot strand a funded redemption.
- `test/adversarial/` has `Reentrancy.t.sol` (a malicious quote token reenters `Vault.buy` and
  `RedemptionEscrow.fundRedemption` during `transferFrom`, and the outer call reverts) and
  `FailureRecovery.t.sol` (an underfunded treasurer, a fee-on-transfer quote rejected at
  funding, and recovery rehearsals in which a quote-token blacklist or a post-funding fee makes
  `claimRedemption` revert atomically with status, escrow balances and Vault inventory
  unchanged, and a plain retry succeeds once the condition lifts).
- `test/invariant/` has `RedemptionInvariant.t.sol` and `handlers/RedemptionHandler.sol`. The
  invariants are supply conservation, escrow RWA and quote accounting, no residual factory role
  and Vault inventory not exceeding supply, fuzzed over bounded mint, buy, request, fund, claim,
  reject, cancel and burn sequences.
- `test/mocks/` has `MockERC20`, `FalseReturnERC20`, `FeeOnTransferERC20`, `ReentrantERC20` and
  `BlacklistableERC20` (under `token/`) and `ERC1271Wallet` (under `wallet/`).
- `test/helpers/TestBase.sol` is the shared fixture. It deploys one full project through
  `RWAFactory` and provides EIP-712 signing helpers that mirror `shared/eip712/types.md`.

### Gas baseline & static analysis

`.gas-snapshot` in `contracts/` is the committed per-test gas baseline. Run `forge snapshot
--diff` before merging a `src/` change to catch unintended gas regressions. Fuzz and invariant
entries vary between runs.

Run `slither . --exclude-dependencies`. The dated triage in
[`docs/security/slither-report.md`](../docs/security/slither-report.md) accepted fixed-helper
delegatecalls, timestamp checks and typehash getter names, plus informational findings for
pragma differences and validation complexity. It does not cover the later ERC-7943 paths;
rerun analysis before release.

### Live E2E broadcast (`script/`)

- `Deploy.s.sol` deploys the helpers, a factory and one project against any chain
  (`forge script script/Deploy.s.sol --broadcast --rpc-url <url>`). Every role defaults to the
  `DEPLOYER_PK` account, and `QUOTE_TOKEN` defaults to a freshly deployed mock. `DEPLOYER_PK`
  falls back to anvil's well-known account 0 key only when `block.chainid == 31337`; elsewhere an
  unset `DEPLOYER_PK` reverts with `RefusingAnvilKeyFallbackOnNonAnvilChain`, so a key anyone can
  derive from this repo is never used on a real chain. `E2EFlow.s.sol` applies the same guard to
  `INVESTOR_PK`.
- `E2EFlow.s.sol` extends `Deploy.s.sol`'s shared logic and drives the full exit scenario with real
  broadcast transactions. `run()` covers deploy, allow, mint, buy, request, fund, claim and burn.
  `requestForCancelDemo()` and `cancelIt(uint256)` are two more entrypoints for the timeout and
  cancel branch; they are separate invocations so the shell can advance the chain clock with
  `cast rpc` between them.
- `e2e.sh` boots a fresh anvil on a random high port (`20000 + RANDOM % 20000` by default, checked
  free with a `/dev/tcp` probe; override with `ANVIL_PORT`), runs the required flow, and greps
  its output for every expected log marker. It then runs the two cancel-branch invocations with
  `cast rpc evm_increaseTime` in between. It exits non-zero on any `forge script` failure or
  missing marker. It also fails, with a message on stderr, if it cannot parse the deployed
  addresses or the request id for the cancel branch.

```sh
bash script/e2e.sh
```
