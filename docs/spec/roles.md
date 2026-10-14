# Role and mutability matrix

Each governance contract maintains its own roles through `AccessControlEnumerable` and
`AccessControlDefaultAdminRules`. Role IDs are frozen.

| Role | ID | Hosted on | Powers |
| --- | --- | --- | --- |
| `DEFAULT_ADMIN_ROLE` | `0x00` (OpenZeppelin default) | all six contracts | grant and revoke the contract's roles; `SupplyController.setAuditor`; `Vault.setTreasury` and `setStrategy`; `RWAToken.setFrozenTokens` and `forcedTransfer` |
| `PAUSER_ROLE` | `keccak256("PAUSER_ROLE")` | RWAToken | `pause` / `unpause`, the project-wide emergency flag |
| `COMPLIANCE_ROLE` | `keccak256("COMPLIANCE_ROLE")` | ComplianceRegistry | `setStatus` / `setStatuses` (wallet status and expiry) |
| `PRICER_ROLE` | `keccak256("PRICER_ROLE")` | FixedPriceStrategy | `setPurchasePrice` / `setRedemptionPrice` |
| `TREASURER_ROLE` | `keccak256("TREASURER_ROLE")` | Vault, RedemptionEscrow | `Vault.withdrawProceeds`, `RedemptionEscrow.fundRedemption` |
| `REDEMPTION_MANAGER_ROLE` | `keccak256("REDEMPTION_MANAGER_ROLE")` | RedemptionEscrow | `rejectRedemption` |

At deployment, `config.admin` holds `DEFAULT_ADMIN_ROLE` on each governance contract and
`PAUSER_ROLE` on the token. Other roles go to their configured holders. Per-contract admin
transfers can later leave different admins.

- Transfer admin authority per contract with `beginDefaultAdminTransfer`, then
  `acceptDefaultAdminTransfer` after `adminTransferDelay`; `grantRole` cannot transfer it.
  Contracts accept a zero delay. `Deploy.s.sol` defaults to 0 on Anvil and one day elsewhere,
  refusing 0 off Anvil unless `ALLOW_ZERO_ADMIN_TRANSFER_DELAY=true`. The admin console's Setup
  form defaults to two days.
  The delay governs admin transfer, not other actions by the current admin.
- Freezing and forced transfers require `DEFAULT_ADMIN_ROLE`. Pausers have no seizure power;
  compliance may use a server hot key, while legal enforcement belongs to the issuer's admin,
  usually a multisig.
- Redemption management follows `REDEMPTION_MANAGER_ROLE`; there is no separate manager setter.
  The escrow's strategy is fixed at deployment; the Vault's can change.
- The auditor is a SupplyController address, not a role. It authorizes mint and burn by EOA or
  ERC-1271 signature. The admin replaces it with `setAuditor`, emitting `AuditorChanged`.
- The factory uses bootstrap `COMPLIANCE_ROLE` to allowlist the Vault and escrow, then renounces
  it before `deploy` returns. It never holds `DEFAULT_ADMIN_ROLE`. Deployment verification
  enumerates the defined governance roles and rejects unexpected holders, including residual
  factory rights.
