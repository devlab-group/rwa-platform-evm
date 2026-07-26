# Role & mutability matrix

| Role                    | keccak256 id                      | Powers                                                            |
| ----------------------- | --------------------------------- | ---------------------------------------------------------------- |
| DEFAULT_ADMIN_ROLE      | OZ default (0x00)                 | grant/revoke roles, set auditor/treasury/strategy/redemptionMgr  |
| PAUSER_ROLE             | keccak256("PAUSER_ROLE")          | pause/unpause the token (project-wide emergency flag)            |
| COMPLIANCE_ROLE         | keccak256("COMPLIANCE_ROLE")      | set wallet status/expiry in ComplianceRegistry                   |
| PRICER_ROLE             | keccak256("PRICER_ROLE")          | update FixedPriceStrategy prices                                 |
| TREASURER_ROLE          | keccak256("TREASURER_ROLE")       | Vault.withdrawProceeds, RedemptionEscrow.fundRedemption          |
| REDEMPTION_MANAGER_ROLE | keccak256("REDEMPTION_MANAGER_ROLE") | RedemptionEscrow.rejectRedemption                             |

- Auditor is stored authority in SupplyController, not a tx-sending role. Changed by admin → `AuditorChanged`.
- Use OZ `AccessControlDefaultAdminRules` for default admin (delay: 0 local, ≥24h prod).
- Factory holds temporary bootstrap rights during deploy and MUST revoke them all before returning.
- Each contract may host its own AccessControl instance, or a shared roles hub — implementer choice,
  but the factory must end with exactly the configured holders and no residual factory rights.
