# contracts: RWA tokenization stack (Foundry)

Non-upgradeable Solidity `^0.8.24` contracts using OpenZeppelin Contracts 5.6.1. Deploy one
`RWAFactory` per platform instance, then call `RWAFactory.deploy(ProjectConfig)` for each
tokenized asset (project).

[`docs/spec/`](../docs/spec/) defines behavior and check order in `contracts.md`, roles and
mutability in `roles.md`, and escrow guards in `redemption-state-machine.md`. Contracts follow
the stable interfaces in `src/interfaces/*.sol`; update the specification before changing one.

## Contract set (`src/`)

| Contract                                | Role                                                                                                                                                                                                                |
| --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ComplianceRegistry.sol`                | Wallet allowlist with optional KYC expiry; pins `Vault` and `RedemptionEscrow` to Allowed with no expiry after wiring.                                                                                              |
| `RWAToken.sol`                          | Pausable ERC-20 with ERC-7943 freeze/seizure controls. Ordinary transfers require Allowed endpoints and unfrozen balances; only SupplyController mints/burns. Controller and escrow are wired once by the deployer. |
| `SupplyController.sol`                  | Validates EIP-712 auditor attestations through OpenZeppelin `EIP712`/`SignatureChecker` (EOA or ERC-1271); mints to and burns from Vault.                                                                           |
| `Vault.sol`                             | Holds RWA inventory; `buy` sells it for quote tokens, subject to an optional minimum purchase, and `withdrawProceeds` pays treasury.                                                                                |
| `pricing/FixedPriceStrategy.sol`        | `PRICER_ROLE` sets purchase/redemption prices; `Math.mulDiv` rounds purchases `Ceil` and redemptions `Floor`.                                                                                                       |
| `pricing/OraclePriceStrategy.sol`       | Prices from a Chainlink-compatible feed with fixed purchase and redemption spreads. Immutable, with no roles.                                                                                                       |
| `RedemptionEscrow.sol`                  | Cash redemption priced by the Vault's current strategy: request, fund, permissionless claim, reject or timed-out cancel.                                                                                            |
| `IncomeDistributor.sol`                 | Pays quote-token income to holders against a Merkle snapshot; sends unclaimed funds to treasury after the claim deadline.                                                                                           |
| `RWAFactory.sol`                        | Deploys and wires a project in one transaction through five stateless helpers.                                                                                                                                      |
| `factory/ComplianceTokenDeployer.sol`   | Creates `ComplianceRegistry` and `RWAToken`.                                                                                                                                                                        |
| `factory/MarketDeployer.sol`            | Creates the configured price strategy and `Vault`.                                                                                                                                                                  |
| `factory/SupplyControllerDeployer.sol`  | Creates `SupplyController`.                                                                                                                                                                                         |
| `factory/RedemptionEscrowDeployer.sol`  | Creates `RedemptionEscrow`.                                                                                                                                                                                         |
| `factory/IncomeDistributorDeployer.sol` | Creates `IncomeDistributor`.                                                                                                                                                                                        |

`script/DeployTestToken.s.sol` deploys the mintable ERC-20 in `src/test/TestToken.sol`.

### Why the deploy helpers exist (EIP-170)

A runtime `new` call embeds child creation code in its caller. Helpers split that code to keep
the reusable factory below EIP-170's 24,576-byte runtime limit. Chains enforcing the limit
reject larger deployments; `forge test` does not enforce it by default.
`test/unit/ContractSizeLimit.t.sol` checks `.code.length <= 24576` for the factory, helpers,
project contracts and `OraclePriceStrategy`.
`forge build --sizes` prints current sizes.

The factory uses `delegatecall` so each child's constructor sees the factory as `msg.sender`.
The registry's bootstrap `COMPLIANCE_ROLE` and the registry/token deployer-only setters depend
on that identity. Deploy the five helpers first, then call
`new RWAFactory(complianceTokenDeployer, marketDeployer, supplyControllerDeployer,
redemptionEscrowDeployer, incomeDistributorDeployer)`.

## Deploy order & wiring (`RWAFactory.deploy`)

1. Validate config; failures use `InvalidConfig(field)` (checks in `docs/spec/contracts.md`).
2. `ComplianceTokenDeployer.deploy()` creates the registry and token. The registry grants
   `COMPLIANCE_ROLE` to `complianceOperator` and the factory for bootstrap. Token controller
   and escrow addresses start unset.
3. `MarketDeployer.deploy()` creates the strategy that `strategyKind` selects, then the Vault.
   An `Oracle` strategy reads the quote token's `decimals()`.
4. `SupplyControllerDeployer.deploy()` creates the controller using the Vault address.
5. `RedemptionEscrowDeployer.deploy()` creates escrow using token and Vault addresses.
6. `IncomeDistributorDeployer.deploy()` creates the income distributor using token and Vault
   addresses.
7. Call `token.setSupplyController(supplyController)` and `token.setRedemptionEscrow(escrow)`.
   One-time, deployer-only setters resolve the construction cycle: controller needs Vault,
   Vault needs token, and controller/escrow need token at construction. The wired values are
   locked, though not declared Solidity `immutable`.
8. `compliance.setSystemAddresses(vault, escrow)` pins the system addresses.
9. Set both to `Allowed` with expiry `0`. Pinning restricts later updates but does not itself
   allowlist them.
10. `compliance.renounceRole(COMPLIANCE_ROLE, address(this))` removes the factory's bootstrap
    grant. Renouncing requires no admin permission.
11. Emit `ProjectDeployed(...)` and return `Deployment`.

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

The only input is `DEPLOYER_PK`; fund its account for six creations (five helpers and factory).
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

| Role                                            | Powers                                                                                                                                                                                                                      |
| ----------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `DEFAULT_ADMIN_ROLE` (each contract with roles) | Manage that contract's roles; `SupplyController.setAuditor`; `Vault.setStrategy`/`setTreasury`/`setMinPurchaseQuote`; `RWAToken.setFrozenTokens`/`forcedTransfer`. Admin transfers follow `AccessControlDefaultAdminRules`. |
| `PAUSER_ROLE` (`RWAToken`)                      | `pause()`/`unpause()`, the project-wide emergency flag.                                                                                                                                                                     |
| `COMPLIANCE_ROLE` (`ComplianceRegistry`)        | `setStatus`/`setStatuses`.                                                                                                                                                                                                  |
| `PRICER_ROLE` (`FixedPriceStrategy`)            | `setPurchasePrice`/`setRedemptionPrice`.                                                                                                                                                                                    |
| `TREASURER_ROLE` (`Vault`, `RedemptionEscrow`)  | `Vault.withdrawProceeds`/`RedemptionEscrow.fundRedemption`.                                                                                                                                                                 |
| `REDEMPTION_MANAGER_ROLE` (`RedemptionEscrow`)  | `rejectRedemption`.                                                                                                                                                                                                         |
| `INCOME_MANAGER_ROLE` (`IncomeDistributor`)     | `createDistribution`/`extendClaimDeadline`.                                                                                                                                                                                 |

The auditor is a stored address replaced by admin-only `SupplyController.setAuditor`.
Anyone with a valid auditor signature can call `mint`/`burn`. Role-bearing contracts each use
`access/AccessControlEnumerableAdminRules.sol`, combining OpenZeppelin `AccessControlEnumerable`
and `AccessControlDefaultAdminRules`. `OraclePriceStrategy` has no roles; there is no shared
roles hub.

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

## Pricing & rounding

`Vault.buy` and `RedemptionEscrow.requestRedemption` both quote through the Vault's current
`strategy`, so `Vault.setStrategy` changes purchase and redemption pricing together. Escrow
fixes `quoteAmount` at request time, so later price or strategy changes do not alter pending
quotes. Purchases round `Ceil` to avoid underpayment; redemptions round `Floor`.

`FixedPriceStrategy` computes
`quote = Math.mulDiv(tokenAmount, price, 10**tokenDecimals, rounding)`. `PRICER_ROLE` sets prices
in quote-token smallest units per whole RWA token. `ArithmeticVectorsTest` in
`test/unit/Vectors.t.sol` checks the strategy and direct `Math.mulDiv` against
`shared/vectors/arithmetic.json`.

`OraclePriceStrategy` reads the value of one whole RWA token in the quote token's currency
from a Chainlink-compatible feed, such as a fund's USD NAV for a USD stablecoin. Purchases add
`purchaseSpreadBps`; redemptions subtract `redemptionSpreadBps`. One `Math.mulDiv` converts feed
and quote decimals. Reads reject non-positive, future-dated or stale answers (`maxAge`),
changed feed decimals and, when configured, a down sequencer or unfinished grace period.
Failures block `buy`, `requestRedemption` and their previews; stored requests can still be
funded, claimed, rejected or cancelled. To change immutable parameters, deploy a replacement
and call `Vault.setStrategy`.

`Vault.minPurchaseQuote` starts at `ProjectConfig.minPurchaseQuote` and the admin can change it
with `setMinPurchaseQuote`. `buy` reverts with `QuoteBelowMinimum` when the purchase quote is
smaller; 0 sets no minimum.

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

## Income distribution (`IncomeDistributor`)

The income manager calls `createDistribution` with a Merkle root, total, past record block,
claim deadline, snapshot URI and SHA-256. The deadline must be at least `MIN_CLAIM_WINDOW`
(7 days) away; the contract pulls exactly `total` quote tokens. It does not check historical
balances. Holders verify the published snapshot's hash and recompute its root.

- A holder claims only for itself, with a proof for the leaf
  `keccak256(bytes.concat(keccak256(abi.encode(account, amount))))`, OpenZeppelin
  `StandardMerkleTree`'s encoding for `["address", "uint256"]`. A claim requires an unpaused
  project, an open window and an Allowed caller, and cannot push the distribution's claimed sum
  above its `total`.
- After the deadline, anyone can call `reclaimUnclaimed` to send the remainder to the Vault's
  current treasury. It reverts while paused, because holders cannot claim during a pause.
- The income manager can only extend a deadline (`extendClaimDeadline`), including while paused
  to give back time a pause took.

`test/unit/IncomeMerkleVectors.t.sol` claims with the roots and proofs that
`@openzeppelin/merkle-tree` generated in `shared/vectors/income-merkle.json`.
`test/invariant/IncomeInvariant.t.sol` checks the contract's quote balance and claimed sums
against funded, claimed and reclaimed amounts.

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
  `Vectors.t.sol`, `SafeAuditorVectors.t.sol`, `IncomeMerkleVectors.t.sol`,
  `ContractSizeLimit.t.sol`, `DeployScript.t.sol` and `DeployFactoryScript.t.sol`. Unit files include `testFuzz_*` tests.
- `test/integration/FullFlow.t.sol`: deploy through locally signed mint, buy, redemption and
  auditor burn; timeout/cancel; ERC-7943 freeze/seizure lifecycle; escrow enforcement protection.
  `OraclePriceFlow.t.sol`: oracle-priced buy and redemption, a stale feed blocking new trades
  but not stored requests, and a strategy switch.
- `test/adversarial/Reentrancy.t.sol`: quote-token callbacks during `transferFrom` revert
  `Vault.buy`/`RedemptionEscrow.fundRedemption`. `FailureRecovery.t.sol` checks insufficient
  funding balance, fee-shortened funding, and atomic claim rollback under a blacklist or
  post-funding fee. Claim retries succeed after the condition lifts.
- `test/invariant/RedemptionInvariant.t.sol` and `handlers/RedemptionHandler.sol`: bounded
  mint, buy, request, fund, claim, reject, cancel and burn sequences check supply conservation,
  escrow accounting, factory bootstrap-role removal and Vault inventory <= supply.
  `IncomeInvariant.t.sol` runs create, claim, extend and reclaim sequences against the income
  distributor's accounting.
- `test/mocks/token/`: `MockERC20`, `FalseReturnERC20`, `FeeOnTransferERC20`, `ReentrantERC20`
  and `BlacklistableERC20`; `test/mocks/wallet/`: `ERC1271Wallet`; `test/mocks/oracle/`:
  `MockAggregatorV3`, a settable price or sequencer feed.
- `test/helpers/TestBase.sol`: factory-deployed fixture and EIP-712 signing helpers matching
  `shared/eip712/types.md`.

### Gas baseline & static analysis

`contracts/.gas-snapshot` records per-test gas. Run `forge snapshot --diff` before merging
`src/` changes to catch regressions. `foundry.toml` pins the fuzz seed for reproducible inputs.

Run `slither . --exclude-dependencies`. The dated
[triage](../docs/security/slither-report.md) accepts fixed-helper delegatecalls, timestamp
checks, typehash getter names, and informational pragma/validation-complexity findings.
It predates `OraclePriceStrategy` and `IncomeDistributor`; rerun analysis before release.

### Live E2E broadcast (`script/`)

- `Deploy.s.sol` creates helpers, factory and project on the supplied chain
  (`forge script script/Deploy.s.sol --broadcast --rpc-url <url>`). Roles default to the
  `DEPLOYER_PK` account and `QUOTE_TOKEN` to a fresh mock. Public Anvil keys are defaults only
  on chain ID `31337`; other chain IDs require explicit `DEPLOYER_PK` and, for `E2EFlow.s.sol`,
  `INVESTOR_PK`, or `RefusingAnvilKeyFallbackOnNonAnvilChain` reverts.
  `MIN_PURCHASE_QUOTE` defaults to 0 and `INCOME_MANAGER` to the deployer. `STRATEGY_KIND=1`
  selects `OraclePriceStrategy`, configured by `PRICE_FEED`, `PRICE_MAX_AGE`,
  `PURCHASE_SPREAD_BPS`, `REDEMPTION_SPREAD_BPS` and optionally `SEQUENCER_UPTIME_FEED` with
  `SEQUENCER_GRACE_PERIOD`; the fixed prices and `PRICER` then default to zero.
- `E2EFlow.s.sol` uses DeployBase and broadcasts deploy, allow, mint, buy, request, fund, claim
  and burn. A separate `distributeIncome()` invocation funds and claims a one-holder income
  distribution. Separate `requestForCancelDemo()` and `cancelIt(uint256)` invocations let the
  shell advance chain time with `cast rpc` between request and cancellation.
- `e2e.sh` starts Anvil on a random high port (`20000 + RANDOM % 20000`, overridden by
  `ANVIL_PORT`), probes for an existing listener, and runs the main flow, the income step and
  the timeout -> cancel branch. It requires expected log markers and parseable deployed
  addresses/request ID, advances time with `cast rpc evm_increaseTime`, and exits nonzero on
  script failure or missing output.

```sh
bash script/e2e.sh
```
