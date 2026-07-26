# Public-testnet qualification runbook

Qualify a release on **at least two materially different** public EVM test environments (e.g. an
OP-stack testnet and an Ethereum L1 testnet) — different fee markets, block times, and reorg
behavior. This is a manual runbook; it needs funded testnet keys and RPC endpoints not present in
the CI sandbox.

## Per environment

1. **Config**: set RPC URL, chain ID, confirmation depth (per that chain's reorg profile),
   fee mode (EIP-1559 or legacy), explorer template, and the quote-token address (deploy a test
   ERC-20 if none). Fund the deployer + hot keys from the faucet.
2. **Deploy**: run `contracts/script/Deploy.s.sol --broadcast --rpc-url <testnet>`; record all
   addresses in `shared/deployments/<chain>-<date>.json`. Verify contracts on the explorer.
3. **Post-deploy verification**: bytecode present, roles correct, Vault + RedemptionEscrow
   allowlisted, factory holds no residual roles.
4. **Full lifecycle** through the server API (as `server/e2e/run_e2e.sh`, but pointed at the
   testnet): compliance → record → `.rwa` → offline sign → broadcast the auditor-signed
   `SupplyController.mint` from the admin wallet and confirm the server observes the `Minted`
   event → buy → request → fund → claim → burn; plus timeout → cancel. Confirm the indexer tracks
   confirmations and finality.
5. **Failure injection**: underfunded fund attempt, quote-token failure on a funded claim (retry),
   an RPC outage (restart the server, confirm it resumes from checkpoint), and — if the testnet
   allows — observe a natural reorg or simulate one and confirm rollback + replay.
6. **Fee/behavior notes**: record gas costs, block time, observed reorg depth, and any
   chain-specific quirks. Confirm the confirmation depth chosen is safe for that chain.

## Sign-off

Qualification passes for a chain when steps 1–5 succeed and step 6 is documented. Record the two
qualified chains, dates, addresses, and operators in the release notes. Do **not** claim a chain
is production-certified without completing this runbook for it.

This runbook's step 5 failure injection is necessarily short (a single restart, a single outage).
It does not replace the separate extended shadow-replay/soak-test gate required before a release
candidate ships — see `soak-runbook.md`.
