# contracts: RWA tokenization stack (Foundry)

Non-upgradeable Solidity `^0.8.24` contracts using OpenZeppelin Contracts 5.6.1. Deploy one
`RWAFactory` per platform instance, then call `RWAFactory.deploy(ProjectConfig)` for each
tokenized asset (project).

[`docs/spec/`](../docs/spec/) defines behavior and check order in `contracts.md`, roles and
mutability in `roles.md`, and escrow guards in `redemption-state-machine.md`. Contracts follow
the stable interfaces in `src/interfaces/*.sol`; update the specification before changing one.

## Contract set (`src/`)

| Contract                               | Role                                                                                                                                                                                                                |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ComplianceRegistry.sol`               | Wallet allowlist with optional KYC expiry; pins `Vault` and `RedemptionEscrow` to Allowed with no expiry after wiring.                                                                                              |
| `RWAToken.sol`                         | Pausable ERC-20 with ERC-7943 freeze/seizure controls. Ordinary transfers require Allowed endpoints and unfrozen balances; only SupplyController mints/burns. Controller and escrow are wired once by the deployer. |
| `SupplyController.sol`                 | Validates EIP-712 auditor attestations through OpenZeppelin `EIP712`/`SignatureChecker` (EOA or ERC-1271); mints to and burns from Vault.                                                                           |
| `Vault.sol`                            | Holds RWA inventory; `buy` sells it for quote tokens and `withdrawProceeds` pays treasury.                                                                                                                          |
| `pricing/FixedPriceStrategy.sol`       | `PRICER_ROLE` sets purchase/redemption prices; `Math.mulDiv` rounds purchases `Ceil` and redemptions `Floor`.                                                                                                       |
| `RedemptionEscrow.sol`                 | Cash redemption: request, fund, permissionless claim, reject or timed-out cancel.                                                                                                                                   |
| `RWAFactory.sol`                       | Deploys and wires a project in one transaction through four stateless helpers.                                                                                                                                      |
| `factory/ComplianceTokenDeployer.sol`  | Creates `ComplianceRegistry` and `RWAToken`.                                                                                                                                                                        |
| `factory/MarketDeployer.sol`           | Creates `FixedPriceStrategy` and `Vault`.                                                                                                                                                                           |
| `factory/SupplyControllerDeployer.sol` | Creates `SupplyController`.                                                                                                                                                                                         |
| `factory/RedemptionEscrowDeployer.sol` | Creates `RedemptionEscrow`.                                                                                                                                                                                         |

`script/DeployTestToken.s.sol` deploys the mintable ERC-20 in `src/test/TestToken.sol`.

### Why the deploy helpers exist (EIP-170)

A runtime `new` call embeds child creation code in its caller. Helpers split that code to keep
the reusable factory below EIP-170's 24,576-byte runtime limit. Chains enforcing the limit
reject larger deployments; `forge test` does not enforce it by default.
`test/unit/ContractSizeLimit.t.sol` checks `.code.length <= 24576` for the factory, four helpers
and six project contracts. `forge build --sizes` prints current sizes.

The factory uses `delegatecall` so each child's constructor sees the factory as `msg.sender`.
The registry's bootstrap `COMPLIANCE_ROLE` and the registry/token deployer-only setters depend
on that identity. Deploy the four helpers first, then call
`new RWAFactory(complianceTokenDeployer, marketDeployer, supplyControllerDeployer,
redemptionEscrowDeployer)`.

## Deploy order & wiring (`RWAFactory.deploy`)

1. Validate config; failures use `InvalidConfig(field)` (checks in `docs/spec/contracts.md`).
2. `ComplianceTokenDeployer.deploy()` creates the registry and token. The registry grants
   `COMPLIANCE_ROLE` to `complianceOperator` and the factory for bootstrap. Token controller
   and escrow addresses start unset.
3. `MarketDeployer.deploy()` creates the strategy and Vault.
4. `SupplyControllerDeployer.deploy()` creates the controller using the Vault address.
5. `RedemptionEscrowDeployer.deploy()` creates escrow using token, Vault and strategy addresses.
6. Call `token.setSupplyController(supplyController)` and `token.setRedemptionEscrow(escrow)`.
   One-time, deployer-only setters resolve the construction cycle: controller needs Vault,
   Vault needs token, and controller/escrow need token at construction. The wired values are
   locked, though not declared Solidity `immutable`.
7. `compliance.setSystemAddresses(vault, escrow)` pins the system addresses.
8. Set both to `Allowed` with expiry `0`. Pinning restricts later updates but does not itself
   allowlist them.
9. `compliance.renounceRole(COMPLIANCE_ROLE, address(this))` removes the factory's bootstrap
   grant. Renouncing requires no admin permission.
10. Emit `ProjectDeployed(...)` and return `Deployment`.

The factory renounces its bootstrap `COMPLIANCE_ROLE` before returning. Other grants follow
`ProjectConfig`; the factory can hold those roles if named as their recipient. Tests use
separate operator addresses and check no factory roles remain:
`ComplianceRegistryTest::test_factoryBootstrapRoleWasRenounced`,
`RWAFactoryTest::test_deploy_factoryRetainsNoPrivileges`,
`RoleEnumerationTest::test_factory_holdsNoBootstrapRoleOnAnyChildPostDeploy`, and
`invariant_factoryHasNoResidualComplianceRole` in `test/invariant/RedemptionInvariant.t.sol`.

## Deploying the factory (bootstrap, `script/DeployFactory.s.sol`)

Deploy the reusable factory once per chain. The admin console then calls `factory.deploy(config)`
from the admin's wallet. The factory is permissionless and has no admin, owner or roles.
For local/staging use, `script/Deploy.s.sol` creates a factory and project together.

```sh
forge script script/DeployFactory.s.sol --rpc-url <your-rpc-url> --broadcast -vv
```

The only input is `DEPLOYER_PK`; fund its account for five creations (four helpers and factory).
On chain ID `31337`, the key defaults to public Anvil account 0. Elsewhere it is required or
`RefusingAnvilKeyFallbackOnNonAnvilChain` reverts before broadcasting.

Copy the logged values into the server's `contract` config block:

| Logged value                      | Server config key          | Meaning                                                                    |
| --------------------------------- | -------------------------- | -------------------------------------------------------------------------- |
| factory address                   | `contract.factory_address` | Bootstrap address; project addresses come from chain/database, not config. |
| deployment block (`block.number`) | `contract.start_block`     | Indexer scan start.                                                        |

## ComplianceRegistry system-address pin

Blocking or expiring Vault/escrow would stop RWA transfers in `buy`, `rejectRedemption` and
`claimRedemption`, potentially leaving funded quote in escrow until re-approval.

Deployer-only `setSystemAddresses(vault, redemptionEscrow)` records both addresses once. Their
subsequent `setStatus`/`setStatuses` updates must use `Allowed` and `validUntil == 0`, or revert
with `SystemAddressCannotBeBlocked`. Pinning expiry matters too: an expired Allowed record
fails `isAllowed`. This protection lasts for the deployment's lifetime.
`test/unit/ComplianceRegistry.t.sol` completes buy, request, fund and claim after a rejected
block attempt to check that the flows still work.

## Roles (`docs/spec/roles.md`)

| Role                                           | Powers                                                                                                                                                                                                |
| ---------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `DEFAULT_ADMIN_ROLE` (six project contracts)   | Manage that contract's roles; `SupplyController.setAuditor`; `Vault.setStrategy`/`setTreasury`; `RWAToken.setFrozenTokens`/`forcedTransfer`. Admin transfers follow `AccessControlDefaultAdminRules`. |
| `PAUSER_ROLE` (`RWAToken`)                     | `pause()`/`unpause()`, the project-wide emergency flag.                                                                                                                                               |
| `COMPLIANCE_ROLE` (`ComplianceRegistry`)       | `setStatus`/`setStatuses`.                                                                                                                                                                            |
| `PRICER_ROLE` (`FixedPriceStrategy`)           | `setPurchasePrice`/`setRedemptionPrice`.                                                                                                                                                              |
| `TREASURER_ROLE` (`Vault`, `RedemptionEscrow`) | `Vault.withdrawProceeds`/`RedemptionEscrow.fundRedemption`.                                                                                                                                           |
| `REDEMPTION_MANAGER_ROLE` (`RedemptionEscrow`) | `rejectRedemption`.                                                                                                                                                                                   |

The auditor is a stored address replaced by admin-only `SupplyController.setAuditor`.
Anyone with a valid auditor signature can call `mint`/`burn`. Each of the six project contracts
has its own `AccessControlDefaultAdminRules`; there is no shared roles hub.

## EIP-712 attestation flow (`SupplyController`)

Domain: `name: "RWA-Supply-Attestation"`, `version: "1"`, current `chainId`, and the controller
as `verifyingContract`. Canonical types and field order are in
[`shared/eip712/types.md`](../shared/eip712/types.md); vectors are in
`shared/vectors/typehashes.json` and `shared/vectors/mint-eip712.json`.

`mint(attestation, signature)` checks, in order: unpaused project, matching auditor, profile and
Vault, unexpired attestation, unused nonce, unused record key, nonzero amount, then a valid
EOA/ERC-1271 signature via `SignatureChecker.isValidSignatureNowCalldata`. It marks nonce and
record key used, calls `token.controllerMint` and emits `Minted`. `burn` substitutes
`operationId` for `recordKey` and checks Vault inventory before signature verification.
`docs/spec/contracts.md` lists the checks and errors.

Mint and burn share one nonce namespace; using a nonce in either blocks the other. Distinct
type hashes prevent reusing a mint signature for burn or vice versa.

`test/unit/Vectors.t.sol` reproduces the golden controller address with `vm.setNonce` and
`vm.prank` (Anvil account 0, nonce 0), then checks typehashes, domain separator, struct hash,
digest and `ECDSA.recover`. It also pins ERC-7943 selectors to `shared/vectors/erc7943-abi.json`
and rounding to `shared/vectors/arithmetic.json`. `test/unit/SafeAuditorVectors.t.sol`
reproduces `shared/vectors/safe-auditor.json` hashes and owner signatures without deploying Safe.

## Pricing & rounding (`FixedPriceStrategy`)

`quote = Math.mulDiv(tokenAmount, price, 10**tokenDecimals, rounding)`. Price is quote-token
smallest units per whole RWA token. Purchases round `Ceil` to avoid underpayment; redemptions
round `Floor`. Escrow fixes `quoteAmount` at request time through
`strategy.quoteRedemption(rwaAmount)`, so later price changes do not alter pending quotes.
`ArithmeticVectorsTest` in `test/unit/Vectors.t.sol` checks both the deployed strategy and
direct `Math.mulDiv` against `shared/vectors/arithmetic.json`.

## Redemption state machine (`RedemptionEscrow`)

`Pending` requests can become `Funded` (then `Completed` on claim), `Rejected` by the redemption
manager, or `Cancelled` by the beneficiary after timeout. The treasurer funds requests.
See the [guard table](../docs/spec/redemption-state-machine.md).
`test/invariant/RedemptionInvariant.t.sol` checks RWA/quote accounting over bounded handler
sequences through `invariant_escrowRwaBackedByOpenRequests` and
`invariant_escrowQuoteBackedByFundedRequests`.

- RWA leaves escrow once through claim, reject or cancel. Only claim pays funded quote, once,
  to the recorded beneficiary; no role has a withdrawal path for unclaimed funded quote.
- Request and funding require an Allowed beneficiary. Claim is permissionless and does not
  recheck that status; later platform blocking alone cannot prevent it. Quote-token restrictions
  can still revert payment. Reject and cancel require `Pending`.
- Timed-out cancel works while paused through `RWAToken.returnEscrowedRWA`, returning escrow's
  RWA to an Allowed beneficiary. Claim uses the same path to Vault but checks pause first;
  other transitions also revert while paused. See `test_cancelRedemption_succeedsWhilePaused`.
- Request RWA and funding quote must arrive with exact balance deltas or revert with
  `RwaDeltaMismatch`/`QuoteDeltaMismatch`. Claim and `Vault.withdrawProceeds` also check quote
  received by the payee, rejecting fee/short transfers atomically.

## Build & test

```sh
cd contracts
forge build              # compile; ABIs land under out/
forge build --sizes      # inspect runtime sizes against EIP-170's 24,576 B limit
forge test               # all suites below
forge test -vv           # verbose test output
forge fmt --check        # formatting check (forge fmt to apply)
forge snapshot           # regenerate .gas-snapshot after a src/ change
forge snapshot --diff    # compare against the committed baseline before merging
bash script/e2e.sh       # live anvil broadcast of the full flow (see below)
```

### Test layout (`forge test`)

- `test/unit/`: one suite per project contract and factory, plus `RoleEnumeration.t.sol`,
  `Vectors.t.sol`, `SafeAuditorVectors.t.sol`, `ContractSizeLimit.t.sol`, `DeployScript.t.sol`
  and `DeployFactoryScript.t.sol`. Unit files include `testFuzz_*` tests.
- `test/integration/FullFlow.t.sol`: deploy through locally signed mint, buy, redemption and
  auditor burn; timeout/cancel; ERC-7943 freeze/seizure lifecycle; escrow enforcement protection.
- `test/adversarial/Reentrancy.t.sol`: quote-token callbacks during `transferFrom` revert
  `Vault.buy`/`RedemptionEscrow.fundRedemption`. `FailureRecovery.t.sol` checks insufficient
  funding balance, fee-shortened funding, and atomic claim rollback under a blacklist or
  post-funding fee. Claim retries succeed after the condition lifts.
- `test/invariant/RedemptionInvariant.t.sol` and `handlers/RedemptionHandler.sol`: bounded
  mint, buy, request, fund, claim, reject, cancel and burn sequences check supply conservation,
  escrow accounting, factory bootstrap-role removal and Vault inventory <= supply.
- `test/mocks/token/`: `MockERC20`, `FalseReturnERC20`, `FeeOnTransferERC20`, `ReentrantERC20`
  and `BlacklistableERC20`; `test/mocks/wallet/`: `ERC1271Wallet`.
- `test/helpers/TestBase.sol`: factory-deployed fixture and EIP-712 signing helpers matching
  `shared/eip712/types.md`.

### Gas baseline & static analysis

`contracts/.gas-snapshot` records per-test gas. Run `forge snapshot --diff` before merging
`src/` changes to catch regressions; fuzz/invariant entries vary between runs.

Run `slither . --exclude-dependencies`. The dated
[triage](../docs/security/slither-report.md) accepts fixed-helper delegatecalls, timestamp
checks, typehash getter names, and informational pragma/validation-complexity findings.
It predates ERC-7943 paths; rerun analysis before release.

### Live E2E broadcast (`script/`)

- `Deploy.s.sol` creates helpers, factory and project on the supplied chain
  (`forge script script/Deploy.s.sol --broadcast --rpc-url <url>`). Roles default to the
  `DEPLOYER_PK` account and `QUOTE_TOKEN` to a fresh mock. Public Anvil keys are defaults only
  on chain ID `31337`; other chain IDs require explicit `DEPLOYER_PK` and, for `E2EFlow.s.sol`,
  `INVESTOR_PK`, or `RefusingAnvilKeyFallbackOnNonAnvilChain` reverts.
- `E2EFlow.s.sol` uses DeployBase and broadcasts deploy, allow, mint, buy, request, fund, claim
  and burn. Separate `requestForCancelDemo()` and `cancelIt(uint256)` invocations let the shell
  advance chain time with `cast rpc` between request and cancellation.
- `e2e.sh` starts Anvil on a random high port (`20000 + RANDOM % 20000`, overridden by
  `ANVIL_PORT`), probes for an existing listener, and runs both flows. It requires expected log
  markers and parseable deployed addresses/request ID, advances time with
  `cast rpc evm_increaseTime`, and exits nonzero on script failure or missing output.

```sh
bash script/e2e.sh
```
