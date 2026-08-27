# contracts — RWA tokenization stack (Foundry)

Solidity `^0.8.24`, OpenZeppelin Contracts 5.6.1, all contracts non-upgradeable. One
`RWAFactory` deployment per platform instance; one `RWAFactory.deploy(ProjectConfig)` call per
tokenized asset ("project").

The specs under [`docs/spec/`](../docs/spec/) describe the intended behavior — `contracts.md`
(check-ordering, per-contract behavior), `roles.md` (role/mutability matrix),
`redemption-state-machine.md` (the state table this repo implements verbatim). Every contract
here implements one of the `src/interfaces/*.sol` interfaces exactly — same function
signatures, events, errors, and structs — so treat those interfaces as the stable boundary: if
something looks like it needs an interface edit, update the spec first rather than working
around it locally.

## Contract set (`src/`)

| Contract | Role |
| --- | --- |
| `ComplianceRegistry.sol` | Wallet allowlist with optional KYC expiry. Pins `Vault`/`RedemptionEscrow` as un-blockable system addresses (see below). |
| `RWAToken.sol` | ERC-20 + `ERC20Pausable`. Compliance-gated ordinary transfers; controller-only mint/burn; deployer-gated one-time `setSupplyController`. |
| `SupplyController.sol` | Validates EIP-712 auditor attestations (OZ `EIP712` + `SignatureChecker`, EOA + ERC-1271) and mints/burns supply through `RWAToken`. |
| `Vault.sol` | Holds RWA inventory; `buy` (sells for the quote token) and `withdrawProceeds`. |
| `pricing/FixedPriceStrategy.sol` | Admin/pricer-settable purchase & redemption prices; `Math.mulDiv` with `Ceil` (purchase) / `Floor` (redemption) rounding. |
| `RedemptionEscrow.sol` | Cash redemption state machine: `request → fund → permissionless claim`, plus `reject`/`cancel`. |
| `RWAFactory.sol` | One-transaction deploy + wiring for a whole project. Thin orchestrator — see "Why the deploy helpers exist" below. |
| `factory/ComplianceTokenDeployer.sol` | Stateless helper: `new`s `ComplianceRegistry` + `RWAToken`. |
| `factory/MarketDeployer.sol` | Stateless helper: `new`s `FixedPriceStrategy` + `Vault`. |
| `factory/SupplyControllerDeployer.sol` | Stateless helper: `new`s `SupplyController`. |
| `factory/RedemptionEscrowDeployer.sol` | Stateless helper: `new`s `RedemptionEscrow`. |

### Why the deploy helpers exist (EIP-170)

Solidity embeds a directly-`new`'d contract's *full creation bytecode* in the deploying
contract's *runtime* bytecode whenever the `new` call sits in a function reachable after
deployment — which `RWAFactory.deploy()` must be, since it's called once per project, not once
total. Bundling all 6 children's `new` calls directly in `RWAFactory` made it 53,571 bytes —
over double the EIP-170 24,576-byte deployed-contract-size limit that every real EVM chain
enforces (anvil doesn't by default, which is exactly how this shipped once before it was
caught by a live `--broadcast`, not by `forge test`).

The fix: split the `new` calls across four small, stateless deployer contracts, invoked from
`RWAFactory.deploy()` via **`delegatecall`** (not `call`). `delegatecall` preserves
`address(this) == RWAFactory` through the helper, so each child's constructor still sees
`msg.sender == address(RWAFactory)` — required for `ComplianceRegistry`'s bootstrap
`COMPLIANCE_ROLE` grant and `RWAToken`'s deployer-gated `setSupplyController` (see below), both
of which key off `msg.sender` at construction time. Current sizes (`forge build --sizes`):

```
RWAFactory                 4,493 B  (was 53,571 B before the split — 12x smaller)
ComplianceTokenDeployer    14,732 B
MarketDeployer              16,135 B
SupplyControllerDeployer    11,015 B
RedemptionEscrowDeployer    11,171 B
```

All comfortably under 24,576 B (minimum margin ~8.4 KB on `MarketDeployer`). Regression guard:
`test/unit/ContractSizeLimit.t.sol` asserts `.code.length <= 24576` for every one of these plus
all 6 production contracts, in plain `forge test` — no anvil, no live chain needed to catch a
future regression.

`RWAFactory`'s constructor takes the four deployer addresses (deploy those first, then
`new RWAFactory(complianceTokenDeployer, marketDeployer, supplyControllerDeployer,
redemptionEscrowDeployer)`) — `IRWAFactory.deploy(ProjectConfig) returns (Deployment)` itself,
the `ProjectDeployed` event, and all deploy-time wiring behavior are unchanged by this; only how
`RWAFactory` itself gets constructed changed.

## Deploy order & wiring (`RWAFactory.deploy`)

1. `ComplianceTokenDeployer.deploy()` → `ComplianceRegistry` (grants `RWAFactory` a temporary
   `COMPLIANCE_ROLE` bootstrap right, and grants the configured `complianceOperator` the same
   role permanently) + `RWAToken` (constructed with `supplyController` left unset).
2. `MarketDeployer.deploy()` → `FixedPriceStrategy` + `Vault`.
3. `SupplyControllerDeployer.deploy()` → `SupplyController` (needs the `Vault` address).
4. `token.setSupplyController(address(supplyController))` — a one-time, deployer-gated setter
   on `RWAToken` (frozen `IRWAToken`, added specifically to resolve the
   Token↔SupplyController↔Vault construction cycle: `SupplyController` needs `Vault`'s address,
   `Vault` needs `RWAToken`'s address, `RWAToken` needs `SupplyController`'s address). Locked
   permanently after the first call; behaves as immutable-after-construction even though it
   isn't the Solidity `immutable` keyword.
5. `RedemptionEscrowDeployer.deploy()` → `RedemptionEscrow` (needs `Vault` + `FixedPriceStrategy`).
6. `compliance.setSystemAddresses(vault, escrow)` — pins both as un-blockable (see below).
7. `compliance.setStatus(vault, Allowed, 0)` and same for `escrow` — actually marks them
   `Allowed` in the registry (step 6 only pins what `setStatus` can change them *to* later; it
   doesn't allow them by itself).
8. `compliance.renounceRole(COMPLIANCE_ROLE, address(this))` — the factory drops its own
   bootstrap right via `renounceRole`, which needs no special permission (unlike `revokeRole`),
   so no extra cleanup step is needed.
9. `emit ProjectDeployed(...)`, return the `Deployment` struct.

**No-residual-roles invariant**: after `deploy()` returns, `RWAFactory` itself holds zero roles
anywhere — no `DEFAULT_ADMIN_ROLE`, no `COMPLIANCE_ROLE`. Every operational role
(`PAUSER_ROLE`, `COMPLIANCE_ROLE`, `PRICER_ROLE`, `TREASURER_ROLE`,
`REDEMPTION_MANAGER_ROLE`) is granted directly to the address named in `ProjectConfig`, not
routed through the factory. Checked continuously by
`test/invariant/RedemptionInvariant.t.sol`'s `invariant_factoryHasNoResidualComplianceRole`
(128,000 fuzzed calls per run, 0 reverts) and pointwise by
`ComplianceRegistryTest::test_factoryBootstrapRoleWasRenounced` /
`RWAFactoryTest::test_deploy_factoryRetainsNoPrivileges`.

## Deploying the factory (bootstrap, `script/DeployFactory.s.sol`)

Before any project can be deployed, the reusable `RWAFactory` must exist on-chain. You deploy
it **once** per chain; every project is then created by calling `factory.deploy(config)` (see
[Deploy order & wiring](#deploy-order--wiring-rwafactorydeploy) above, and `script/Deploy.s.sol`
for a full one-shot project deploy). The factory is permissionless deploy machinery — its
constructor takes no admin/owner and grants no roles.

**Run it:**

```sh
forge script script/DeployFactory.s.sol --rpc-url <your-rpc-url> --broadcast -vv
```

**Input** — one env var, `DEPLOYER_PK`, the private key to broadcast from. The account only needs
enough gas for five `CREATE`s (the four stateless child deployers — `ComplianceTokenDeployer`,
`MarketDeployer`, `SupplyControllerDeployer`, `RedemptionEscrowDeployer` — plus the `RWAFactory`
that wires them). On anvil (`chainid 31337`) `DEPLOYER_PK` defaults to the well-known anvil
account 0; on any other chain it **must** be set explicitly, or the run reverts with
`RefusingAnvilKeyFallbackOnNonAnvilChain` rather than broadcasting from a key printed in every
anvil banner.

**Output** — the script logs the two values the server needs in its config `contract` block:

| Logged value | Server config key | Meaning |
|---|---|---|
| factory address | `contract.factory_address` | the one address the server needs to bootstrap; all deployed-project addresses are then read from the chain/DB, not config |
| deployment block (`block.number`) | `contract.start_block` | the block the indexer begins scanning from |

After this, point the server at the factory (`contract.factory_address` + `contract.start_block`),
and deploy individual projects through it via the server's deploy endpoint or `script/Deploy.s.sol`.

## ComplianceRegistry system-address pin

Without it, `COMPLIANCE_ROLE` could set `Vault` or `RedemptionEscrow` to `Blocked` exactly like
a user wallet — bricking `buy`/`claimRedemption` (both are `RWAToken` transfer
counterparties) and, worse, trapping a `Funded` redemption's already-pulled-in quote until the
address was manually re-allowed. A single fat-fingered batch entry or a compromised compliance
key becomes a system-wide self-DoS with a disproportionate blast radius.

Remediation: `ComplianceRegistry.setSystemAddresses(vault, redemptionEscrow)` — one-time,
deployer-gated (same pattern as `RWAToken.setSupplyController`) — records the two addresses.
`setStatus`/`setStatuses` then reject any attempt to set a pinned system address to a status
other than `Allowed` with `SystemAddressCannotBeBlocked`, effectively pinning them `Allowed` for
the life of the deployment. `IComplianceRegistry.setStatus/setStatuses` signatures are
unchanged; the interface only grew the new setter, an `isSystemAddress` view, and three errors.
Regression tests in `test/unit/ComplianceRegistry.t.sol` include one that drives a full
`buy → request → fund → claim` sequence to completion immediately after a rejected block
attempt — proving the pin doesn't just *reject the block*, the normal flows genuinely keep
working.

## Roles (`docs/spec/roles.md`)

| Role | Powers |
| --- | --- |
| `DEFAULT_ADMIN_ROLE` | Grant/revoke roles, set auditor/treasury/strategy/redemption-manager (per contract, via `AccessControlDefaultAdminRules`). |
| `PAUSER_ROLE` (`RWAToken`) | `pause()`/`unpause()` — the single project-wide emergency flag. |
| `COMPLIANCE_ROLE` (`ComplianceRegistry`) | `setStatus`/`setStatuses`. |
| `PRICER_ROLE` (`FixedPriceStrategy`, `Vault`) | Update purchase/redemption prices. |
| `TREASURER_ROLE` (`Vault`, `RedemptionEscrow`) | `Vault.withdrawProceeds`; `RedemptionEscrow.fundRedemption`. |
| `REDEMPTION_MANAGER_ROLE` (`RedemptionEscrow`) | `rejectRedemption`. |

The auditor is a stored authority on `SupplyController` (`setAuditor`, admin-gated), not a
tx-sending role — every `mint`/`burn` call is permissionless as long as it carries a valid
auditor signature. Each contract hosts its own `AccessControlDefaultAdminRules` instance (no
shared roles hub); the factory wires every role directly to the address in `ProjectConfig`, per
the no-residual-roles invariant above.

## EIP-712 attestation flow (`SupplyController`)

Domain: `name: "RWA-Supply-Attestation"`, `version: "1"`, current `chainId`, `verifyingContract`
= the `SupplyController` instance. Full canonical type strings and field order live in
[`shared/eip712/types.md`](../shared/eip712/types.md); the frozen typehash/vector values are in
`shared/vectors/typehashes.json` and `shared/vectors/mint-eip712.json`.

`mint(attestation, signature)` check order (see `docs/spec/contracts.md` for the exact
enumeration, mirrored 1:1 in `SupplyController.sol`): not paused → attestation auditor matches
stored auditor → profile digest matches → vault matches → not expired → nonce unused → record
key unused → amount nonzero → `SignatureChecker.isValidSignatureNow` (EOA or ERC-1271) →
effects (mark nonce/record-key used) → `token.controllerMint` → `emit Minted`. `burn` is the
same shape with `operationId` replacing `recordKey` and an added
`amount <= token.balanceOf(vault)` check. The nonce namespace is **shared** across mint and
burn — reusing a nonce from either call reverts the other. Distinct type hashes prevent
cross-type replay (a mint signature can never validate as a burn attestation or vice versa).

`test/unit/Vectors.t.sol` reproduces the frozen golden vector byte-for-byte: it deploys
`SupplyController` at the exact `verifyingContract` address from the vector
(`vm.setNonce` + `vm.prank` from anvil account 0 at nonce 0), then asserts the typehashes,
domain separator, `hashStruct`, digest, and `ECDSA.recover(digest, signature) == auditor` all
match the committed vector exactly — no ffi/JSON parsing needed.

## Pricing & rounding (`FixedPriceStrategy`)

`quote = Math.mulDiv(tokenAmount, price, 10**tokenDecimals, rounding)` — `price` is quote-token
smallest units per whole RWA token. **Purchase rounds `Ceil`** (the vault is never underpaid);
**redemption rounds `Floor`** (the vault never overpays). `RedemptionEscrow.requestRedemption`
snapshots `quoteAmount` at request time via `strategy.quoteRedemption(rwaAmount)` — later price
changes don't affect an already-pending request. `test/unit/Vectors.t.sol`'s
`ArithmeticVectorsTest` checks all 3 cases from `shared/vectors/arithmetic.json` (whole-token,
fractional round-up-vs-down, odd-amount rounding) against both the deployed contract and a
direct `Math.mulDiv` call.

## Redemption state machine (`RedemptionEscrow`)

`None → Pending → {Funded → Completed | Rejected | Cancelled}`. Full guard table in
[`docs/spec/redemption-state-machine.md`](../docs/spec/redemption-state-machine.md); notable
invariants also covered by `test/invariant/RedemptionInvariant.t.sol`
(`invariant_escrowRwaBackedByOpenRequests`, `invariant_escrowQuoteBackedByFundedRequests`):

- A request's RWA leaves escrow exactly once (claim/reject/cancel); a funded request's quote is
  paid exactly once, only at claim, only to the recorded beneficiary. No function exists for any
  role to withdraw funded-but-unclaimed quote.
- `claimRedemption` is permissionless and does **not** re-check the beneficiary's compliance
  status — compliance is enforced once, at funding time. If the beneficiary is later
  de-whitelisted, `reject`/`cancel` revert (RWA stays escrowed) but a already-`Funded` claim
  still completes.
- `cancelRedemption` has no explicit `token.paused()` check in code (matching the frozen
  Guard-column table, which omits one for this transition only) — but it's blocked during a
  pause anyway, because `RWAToken` is `ERC20Pausable` and blocks *all* transfers
  unconditionally, so the RWA-return leg reverts with `Pausable.EnforcedPause()` regardless.
  Documented in `RedemptionEscrow.sol`'s NatSpec and covered by
  `test_cancelRedemption_revertsWhilePausedViaTokenTransfer`.
- Every transfer asserts an exact balance delta on the pull-in legs (`requestRedemption`,
  `fundRedemption`) — a fee-on-transfer or short-transferring quote/RWA token reverts the whole
  call rather than silently under-crediting the escrow (`RwaDeltaMismatch`/`QuoteDeltaMismatch`).

## Build & test

```sh
cd contracts
forge build              # compile; ABIs land under out/
forge build --sizes      # confirm every contract is under the EIP-170 24,576 B limit
forge test                # 207 tests, all suites below
forge test -vv             # with traces
forge fmt --check         # formatting check (forge fmt to apply)
forge snapshot             # regenerate .gas-snapshot after a src/ change
forge snapshot --diff     # compare against the committed baseline before merging
bash script/e2e.sh         # live anvil broadcast of the full flow (see below)
```

### Test layout (207 tests, `forge test`)

- `test/unit/` — one file per contract (`ComplianceRegistry`, `RWAToken`, `SupplyController`,
  `Vault`, `FixedPriceStrategy`, `RedemptionEscrow`, `RWAFactory`), plus `Vectors.t.sol` (EIP-712
  + arithmetic golden vectors), `ContractSizeLimit.t.sol` (EIP-170 regression guard), and
  `DeployScript.t.sol` (the anvil-dev-key chain-id guard, see below). One fuzz test lives inline
  in `FixedPriceStrategy.t.sol` (`testFuzz_quotePurchaseNeverBelowExactDivision`) rather than a
  separate `test/fuzz/` directory.
- `test/integration/FullFlow.t.sol` — deploy → allow → mint (locally-signed attestation) → buy
  → requestRedemption → fund → claim → auditor burn of returned Vault inventory; a
  timeout→cancel branch; a pause-blocks-every-entry-point branch.
- `test/adversarial/` — `Reentrancy.t.sol` (malicious quote token reentering `Vault.buy` and
  `RedemptionEscrow.fundRedemption`, proving the actual revert reason is
  `ReentrancyGuardReentrantCall()`) and `FailureRecovery.t.sol` (underfunded treasurer,
  fee-on-transfer quote rejected at funding, and the funded-claim **blacklist recovery
  rehearsal**: a quote token blacklists the beneficiary after funding, `claimRedemption`
  reverts atomically with status/escrow-balances/Vault-inventory all provably untouched, then a
  plain retry after the blacklist lifts succeeds and pays the exact beneficiary).
- `test/invariant/` — `RedemptionInvariant.t.sol` + `handlers/RedemptionHandler.sol`: 5
  invariants (supply conservation, escrow RWA/quote accounting, no residual factory roles,
  vault inventory ≤ supply) fuzzed across bounded mint/buy/request/fund/claim/reject/cancel/burn
  sequences, 256 runs × 500 calls each, 0 reverts.
- `test/mocks/` — `MockERC20`, `FalseReturnERC20`, `FeeOnTransferERC20`, `ReentrantERC20`,
  `BlacklistableERC20` (token/), `ERC1271Wallet` (wallet/) — used across the adversarial and
  unit suites above.
- `test/helpers/TestBase.sol` — shared fixture (deploys one full project via `RWAFactory`) +
  EIP-712 signing helpers that mirror `shared/eip712/types.md` byte-for-byte, reused by every
  suite above.

### Gas baseline & static analysis

`.gas-snapshot` (root of `contracts/`) is the committed per-test gas baseline — re-run
`forge snapshot --diff` before merging any change to `src/` to catch unintended gas
regressions.

Slither (`slither . --exclude-dependencies`) is run over the tree. The findings that remain are
all reviewed and either intentional-by-design — the delegatecall-to-fixed-helper deploy
pattern, the spec-required timestamp comparisons, the `ISupplyController` typehash getter
naming — or purely informational (OpenZeppelin's own differing pragma floors,
`RWAFactory._validateConfig`'s flat validation branches). None is an exploitable vulnerability.

### Live E2E broadcast (`script/`)

- `Deploy.s.sol` — standalone, env-configurable deploy against any chain
  (`forge script script/Deploy.s.sol --broadcast --rpc-url <url>`). Every role defaults to a
  single `DEPLOYER_PK`; `QUOTE_TOKEN` deploys a fresh mock if not supplied. `DEPLOYER_PK` itself
  only silently falls back to anvil's well-known account-0 key when `block.chainid == 31337`
  (anvil's default) — on any other chain, an unset `DEPLOYER_PK` reverts with
  `RefusingAnvilKeyFallbackOnNonAnvilChain` rather than deploying with a key anyone can derive
  from this repo's own source. `E2EFlow.s.sol` applies the same guard to `INVESTOR_PK`.
- `E2EFlow.s.sol` — extends `Deploy.s.sol`'s shared logic and drives the full exit scenario
  with real broadcast transactions: `run()` for deploy→allow→mint→buy→request→fund→claim→burn;
  `requestForCancelDemo()`/`cancelIt(uint256)` as two more entrypoints for the timeout→cancel
  branch (split out because `vm.rpc("evm_increaseTime", ...)` reliably reverts on this
  forge/anvil build — the time-advance instead happens at the shell level via `cast rpc`
  between two script invocations).
- `e2e.sh` — orchestrates all of the above against a freshly-booted anvil: boots anvil on a
  **random high port** (`20000 + RANDOM % 20000` by default, preflight-checked free via a
  portable `/dev/tcp` probe before starting — override with `ANVIL_PORT`), runs the required
  flow, greps its output for every expected log marker, then runs the two-step cancel-demo
  invocations with `cast rpc evm_increaseTime` in between, and exits non-zero loudly on any
  `forge script` failure or missing marker. Exit 0 means every step actually mined on a live
  chain, not just passed in `forge test`'s VM.

```sh
bash script/e2e.sh
```
