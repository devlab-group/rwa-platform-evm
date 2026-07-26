// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {FixedPriceStrategy} from "../pricing/FixedPriceStrategy.sol";
import {Vault} from "../Vault.sol";

/// @title MarketDeployer
/// @notice Stateless deploy logic for FixedPriceStrategy + Vault. See ComplianceTokenDeployer
///         for why RWAFactory's `new` calls are split across helper contracts like this one.
/// @dev Called via `delegatecall` from RWAFactory for consistency with the other deploy
///      helpers, though neither FixedPriceStrategy nor Vault actually reads `msg.sender` for
///      anything privileged (their admin/role addresses are explicit constructor params) —
///      unlike ComplianceTokenDeployer, plain `call` would also have been safe here. Holds no
///      state of its own.
contract MarketDeployer {
    function deploy(IRWAFactory.ProjectConfig calldata config, address token)
        external
        returns (address strategy, address vault)
    {
        strategy = address(
            new FixedPriceStrategy(
                config.decimals,
                config.purchasePricePerWholeToken,
                config.redemptionPricePerWholeToken,
                config.pricer,
                config.admin,
                config.adminTransferDelay
            )
        );
        vault = address(
            new Vault(
                token,
                config.quoteToken,
                strategy,
                config.treasury,
                config.admin,
                config.treasurer,
                config.pricer,
                config.adminTransferDelay
            )
        );
    }
}
