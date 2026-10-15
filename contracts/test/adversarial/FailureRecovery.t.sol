// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {IRedemptionEscrow} from "../../src/interfaces/IRedemptionEscrow.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {RWAToken} from "../../src/RWAToken.sol";
import {RedemptionEscrow} from "../../src/RedemptionEscrow.sol";
import {Vault} from "../../src/Vault.sol";
import {BlacklistableERC20} from "../mocks/token/BlacklistableERC20.sol";
import {FeeOnTransferERC20} from "../mocks/token/FeeOnTransferERC20.sol";

/// @notice Tests atomic funding/claim failures from insufficient treasurer balance, quote-token
///         blacklisting and transfer fees, with successful claim retries after restrictions lift
contract FailureRecoveryTest is TestBase {
    function setUp() public override {
        super.setUp();
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 100 ether, "FAIL-REC-1", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));
        vm.prank(address(vault));
        token.transfer(investor, 40 ether);
        vm.prank(investor);
        token.approve(address(escrow), 40 ether);
    }

    /// @dev Insufficient treasurer balance reverts funding, leaving the request Pending and its RWA escrowed
    function test_fundRedemption_underfundedTreasuryReverts() public {
        vm.prank(investor);
        uint256 id = escrow.requestRedemption(40 ether, 0, uint64(block.timestamp + 1 hours));

        // Treasurer has approved but has no quote balance
        vm.prank(treasurer);
        quoteToken.approve(address(escrow), type(uint256).max);
        assertEq(quoteToken.balanceOf(treasurer), 0);

        uint256 escrowRwaBefore = token.balanceOf(address(escrow));
        vm.prank(treasurer);
        vm.expectRevert(); // OZ ERC20InsufficientBalance from the pull-in transferFrom
        escrow.fundRedemption(id);

        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Pending));
        assertEq(token.balanceOf(address(escrow)), escrowRwaBefore);
    }

    /// @dev Blacklisting the beneficiary after funding reverts claim without changing status or balances;
    ///      retry succeeds after the blacklist lifts
    function test_claimRedemption_blacklistedBeneficiary_recoversAfterUnblacklist() public {
        BlacklistableERC20 busd = new BlacklistableERC20();
        (address freshTokenAddr, address freshVaultAddr,,, address freshEscrowAddr) =
            _deployWithQuoteToken(address(busd), "blacklist-demo");
        RWAToken freshToken = RWAToken(freshTokenAddr);
        RedemptionEscrow freshEscrow = RedemptionEscrow(freshEscrowAddr);

        vm.prank(freshVaultAddr);
        freshToken.transfer(investor, 40 ether);
        vm.prank(investor);
        freshToken.approve(freshEscrowAddr, 40 ether);

        vm.prank(investor);
        uint256 id = freshEscrow.requestRedemption(40 ether, 0, uint64(block.timestamp + 1 hours));
        uint256 quoteAmount = freshEscrow.getRedemption(id).quoteAmount;

        busd.mint(treasurer, quoteAmount);
        vm.prank(treasurer);
        busd.approve(freshEscrowAddr, quoteAmount);
        vm.prank(treasurer);
        freshEscrow.fundRedemption(id);
        assertEq(uint8(freshEscrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));

        // Blacklist hits the beneficiary after funding
        busd.setBlacklisted(investor, true);

        uint256 escrowRwaBefore = freshToken.balanceOf(freshEscrowAddr);
        uint256 escrowQuoteBefore = busd.balanceOf(freshEscrowAddr);
        uint256 vaultInventoryBefore = Vault(freshVaultAddr).inventory();

        vm.expectRevert(abi.encodeWithSelector(BlacklistableERC20.AccountBlacklisted.selector, investor));
        freshEscrow.claimRedemption(id);

        // Atomic rollback preserves status, escrow balances and Vault inventory
        IRedemptionEscrow.RedemptionRequest memory r = freshEscrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));
        assertEq(freshToken.balanceOf(freshEscrowAddr), escrowRwaBefore);
        assertEq(busd.balanceOf(freshEscrowAddr), escrowQuoteBefore);
        assertEq(Vault(freshVaultAddr).inventory(), vaultInventoryBefore);

        // Lift the blacklist and retry the same claim
        busd.setBlacklisted(investor, false);
        freshEscrow.claimRedemption(id);

        r = freshEscrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));
        assertEq(Vault(freshVaultAddr).inventory(), vaultInventoryBefore + 40 ether);
        assertEq(busd.balanceOf(investor), quoteAmount);
    }

    /// @dev Funding rejects a fee-shortened quote transfer through its exact balance-delta check
    function test_fundRedemption_feeOnTransferQuoteRejected() public {
        FeeOnTransferERC20 feeToken = new FeeOnTransferERC20(500); // 5% fee
        (address freshTokenAddr, address freshVaultAddr,,, address freshEscrowAddr) =
            _deployWithQuoteToken(address(feeToken), "fee-fund-demo");
        RWAToken freshToken = RWAToken(freshTokenAddr);
        RedemptionEscrow freshEscrow = RedemptionEscrow(freshEscrowAddr);

        vm.prank(freshVaultAddr);
        freshToken.transfer(investor, 40 ether);
        vm.prank(investor);
        freshToken.approve(freshEscrowAddr, 40 ether);

        vm.prank(investor);
        uint256 id = freshEscrow.requestRedemption(40 ether, 0, uint64(block.timestamp + 1 hours));
        uint256 quoteAmount = freshEscrow.getRedemption(id).quoteAmount;

        feeToken.mint(treasurer, quoteAmount);
        vm.prank(treasurer);
        feeToken.approve(freshEscrowAddr, quoteAmount);

        vm.prank(treasurer);
        vm.expectRevert(); // QuoteDeltaMismatch(expected, actual), with actual < expected after the fee
        freshEscrow.fundRedemption(id);

        IRedemptionEscrow.RedemptionRequest memory r = freshEscrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Pending));
    }

    /// @dev A fee enabled after full funding must still revert claim on short payment, preserving
    ///      Funded status and escrow balances. Disabling the fee permits the same retry.
    function test_claimRedemption_feeEnabledAfterFunding_recoversOnceDisabled() public {
        FeeOnTransferERC20 feeToken = new FeeOnTransferERC20(500); // 5% fee, starts enabled
        feeToken.setFeeEnabled(false); // Disable for full funding
        (address freshTokenAddr, address freshVaultAddr,,, address freshEscrowAddr) =
            _deployWithQuoteToken(address(feeToken), "fee-claim-demo");
        RWAToken freshToken = RWAToken(freshTokenAddr);
        RedemptionEscrow freshEscrow = RedemptionEscrow(freshEscrowAddr);

        vm.prank(freshVaultAddr);
        freshToken.transfer(investor, 40 ether);
        vm.prank(investor);
        freshToken.approve(freshEscrowAddr, 40 ether);

        vm.prank(investor);
        uint256 id = freshEscrow.requestRedemption(40 ether, 0, uint64(block.timestamp + 1 hours));
        uint256 quoteAmount = freshEscrow.getRedemption(id).quoteAmount;

        feeToken.mint(treasurer, quoteAmount);
        vm.prank(treasurer);
        feeToken.approve(freshEscrowAddr, quoteAmount);
        vm.prank(treasurer);
        freshEscrow.fundRedemption(id);
        assertEq(uint8(freshEscrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));
        assertEq(feeToken.balanceOf(freshEscrowAddr), quoteAmount);

        // Enable the fee after funding; safeTransfer alone would deliver 5% less
        feeToken.setFeeEnabled(true);

        uint256 escrowRwaBefore = freshToken.balanceOf(freshEscrowAddr);
        uint256 escrowQuoteBefore = feeToken.balanceOf(freshEscrowAddr);
        uint256 vaultInventoryBefore = Vault(freshVaultAddr).inventory();

        vm.expectRevert(); // QuoteDeltaMismatch(expected, actual), with actual < expected after the fee
        freshEscrow.claimRedemption(id);

        // Atomic rollback preserves status, escrow balances and Vault inventory
        IRedemptionEscrow.RedemptionRequest memory r = freshEscrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));
        assertEq(freshToken.balanceOf(freshEscrowAddr), escrowRwaBefore);
        assertEq(feeToken.balanceOf(freshEscrowAddr), escrowQuoteBefore);
        assertEq(Vault(freshVaultAddr).inventory(), vaultInventoryBefore);

        // Disable the fee and retry the same claim
        feeToken.setFeeEnabled(false);
        freshEscrow.claimRedemption(id);

        r = freshEscrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));
        assertEq(Vault(freshVaultAddr).inventory(), vaultInventoryBefore + 40 ether);
        assertEq(feeToken.balanceOf(investor), quoteAmount);
    }
}
