# Public-testnet qualification runbook

Qualify each release on at least two materially different public EVM test networks, such as an
OP-stack testnet and an Ethereum L1 testnet. Choose networks with different fee markets, block
times and reorg behavior. This is a manual procedure requiring funded testnet wallets and RPC
endpoints; CI's local-chain tests do not replace it.

## Per environment

1. Configure the server's YAML file:

   - `chain.rpc_url` and `chain.id`;
   - `chain.confirmations`, chosen for the network's reorg profile;
   - `chain.fee_mode`, either `eip1559` or `legacy`;
   - a fee cap through `chain.max_fee_per_gas_wei`, `chain.max_tx_total_cost_wei`, or both;
   - `security.admin_address` and `contract.project_id`.

   With `environment: production`, startup rejects zero confirmations and requires a fee cap
   unless `production_overrides.allow_unbounded_fees` is set. Choose a quote token or deploy a
   test ERC-20 with `contracts/script/DeployTestToken.s.sol`. Fund the admin, compliance,
   treasurer and test investor wallets from a faucet.

2. Deploy through either path in the [operator guide, §3](operator-guide.md#3-deploy). Run
   deployment scripts with `forge script ... --rpc-url <testnet> --broadcast`. Set
   `contract.factory_address` and `contract.start_block` to the factory address and its
   deployment block. Deploy the project from the `security.admin_address` wallet so the server
   can adopt it. Verify the contracts on the network's block explorer and record their addresses.

3. Confirm post-deployment verification. Wait for the server to adopt the deployment and mark
   it `Active`, then review the Security screen. The checks include bytecode at each project
   contract, role holders, the Vault and escrow's permanent registry approval, and removal of
   the factory's platform roles. The [operator guide, §3](operator-guide.md#3-deploy), step 4,
   lists the checks.

4. Complete the lifecycle manually, using `server/e2e/run_e2e.sh` as the local Anvil reference:
   ownership challenge and compliance approval → asset record → `.rwa` package → offline
   signature → admin-wallet submission of the auditor-signed `SupplyController.mint` → server
   observation of `Minted` → purchase → redemption request → funding → claim → burn.

   Use a second request to test timeout and cancellation. The factory requires a timeout of at
   least one day. The local script advances Anvil time; on a public testnet, leave the request
   Pending until the configured timeout elapses. The script does not cover burning, so perform
   that step manually too. Verify event indexing and the API's confirmation and finality behavior.

5. Inject failures and verify recovery:

   - Attempt funding with insufficient quote tokens.
   - Make a Funded claim fail at the quote-token level, resolve the refusal and retry. Follow
     [operator-guide.md §7](operator-guide.md#rotation-and-recovery-rehearsal), rehearsal step 4.
   - Interrupt RPC access, restart the server and confirm that indexing resumes from its checkpoint.
   - Where the network permits, observe a natural reorg or simulate one and check rollback and replay.

6. Record gas costs, block times, the deepest observed reorg and chain-specific behavior.
   Document why the chosen confirmation depth is appropriate for the network.

## Sign-off

A network passes qualification when steps 1 through 5 succeed and step 6 is documented.
Record the qualified networks, dates, contract addresses and operators in the release notes.
Do not describe a network as production-certified without completing this runbook for it.

Step 5 tests short failure drills, including one outage and restart. Before shipping a release
candidate, also complete the [shadow-replay and soak-test gate](soak-runbook.md).
