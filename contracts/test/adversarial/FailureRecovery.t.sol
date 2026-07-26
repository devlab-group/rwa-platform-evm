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

/// @notice Attack tests: failure injection on the redemption funding/claim path, and proof
///         that each failure is recoverable rather than a stuck state — no
///         role can force a loss, and a retry after the underlying condition clears succeeds
///         cleanly. Covers: underfunded treasury, quote-token blacklist during a funded claim
///         (with an explicit recovery rehearsal once the blacklist lifts), and fee-on-transfer
///         quote-token rejection at funding time.
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

    /// @dev A treasurer with insufficient quote balance cannot fund a redemption: the pull-in
    ///      leg reverts, the request stays Pending, and the beneficiary's RWA stays exactly
    ///      where it was (escrowed, not lost, not double-counted).
    function test_fundRedemption_underfundedTreasuryReverts() public {
        vm.prank(investor);
        uint256 id = escrow.requestRedemption(40 ether, 0, uint64(block.timestamp + 1 hours));

        // Treasurer approves but never received any quote token — balance is zero.
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

    /// @dev The funded-redemption recovery rehearsal: quote token blacklists the beneficiary
    ///      after funding (e.g. a sanctions hit on the off-chain rail backing the quote
    ///      token), claimRedemption reverts and leaves Funded status + escrowed funds
    ///      untouched, then once the blacklist lifts a plain retry of the same claim succeeds
    ///      — proving the failure is retryable/recoverable, not a stuck or lost state.
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

        // Blacklist hits the beneficiary after funding.
        busd.setBlacklisted(investor, true);

        uint256 escrowRwaBefore = freshToken.balanceOf(freshEscrowAddr);
        uint256 escrowQuoteBefore = busd.balanceOf(freshEscrowAddr);
        uint256 vaultInventoryBefore = Vault(freshVaultAddr).inventory();

        vm.expectRevert(abi.encodeWithSelector(BlacklistableERC20.AccountBlacklisted.selector, investor));
        freshEscrow.claimRedemption(id);

        // Reverted atomically: status, escrow balances, and Vault inventory are all untouched.
        IRedemptionEscrow.RedemptionRequest memory r = freshEscrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));
        assertEq(freshToken.balanceOf(freshEscrowAddr), escrowRwaBefore);
        assertEq(busd.balanceOf(freshEscrowAddr), escrowQuoteBefore);
        assertEq(Vault(freshVaultAddr).inventory(), vaultInventoryBefore);

        // Recovery: blacklist lifts, the exact same claim call is retried and succeeds.
        busd.setBlacklisted(investor, false);
        freshEscrow.claimRedemption(id);

        r = freshEscrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));
        assertEq(Vault(freshVaultAddr).inventory(), vaultInventoryBefore + 40 ether);
        assertEq(busd.balanceOf(investor), quoteAmount);
    }

    /// @dev A fee-on-transfer quote token can never fund a redemption for less than the
    ///      recorded quoteAmount: the exact balance-delta check in fundRedemption's pull-in
    ///      leg rejects it outright rather than silently under-funding the escrow.
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
        vm.expectRevert(); // QuoteDeltaMismatch(expected, actual) — actual < expected after fee
        freshEscrow.fundRedemption(id);

        IRedemptionEscrow.RedemptionRequest memory r = freshEscrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Pending));
    }

    /// @dev `fundRedemption`'s exact-delta check only protects the *inbound* leg. A
    ///      quote token whose fee turns on only after funding — the treasurer paid in full,
    ///      the escrow really holds `quoteAmount` — must still be caught on the *outbound* leg:
    ///      `claimRedemption` cannot mark the request `Completed` while underpaying the
    ///      beneficiary. Reverts atomically (status stays Funded, escrow balance untouched),
    ///      then recovers once the fee is disabled again, same recovery-rehearsal shape as the
    ///      blacklist test above.
    function test_claimRedemption_feeEnabledAfterFunding_recoversOnceDisabled() public {
        FeeOnTransferERC20 feeToken = new FeeOnTransferERC20(500); // 5% fee, starts enabled
        feeToken.setFeeEnabled(false); // disabled for funding — escrow must receive the full amount
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

        // Fee turns on after funding — the escrow's own balance is untouched, but a plain
        // `safeTransfer` to the beneficiary would now silently deliver 5% less.
        feeToken.setFeeEnabled(true);

        uint256 escrowRwaBefore = freshToken.balanceOf(freshEscrowAddr);
        uint256 escrowQuoteBefore = feeToken.balanceOf(freshEscrowAddr);
        uint256 vaultInventoryBefore = Vault(freshVaultAddr).inventory();

        vm.expectRevert(); // QuoteDeltaMismatch(expected, actual) — actual < expected after fee
        freshEscrow.claimRedemption(id);

        // Reverted atomically: status, escrow balances, and Vault inventory are all untouched.
        IRedemptionEscrow.RedemptionRequest memory r = freshEscrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));
        assertEq(freshToken.balanceOf(freshEscrowAddr), escrowRwaBefore);
        assertEq(feeToken.balanceOf(freshEscrowAddr), escrowQuoteBefore);
        assertEq(Vault(freshVaultAddr).inventory(), vaultInventoryBefore);

        // Recovery: fee turns back off, the exact same claim call is retried and succeeds.
        feeToken.setFeeEnabled(false);
        freshEscrow.claimRedemption(id);

        r = freshEscrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));
        assertEq(Vault(freshVaultAddr).inventory(), vaultInventoryBefore + 40 ether);
        assertEq(feeToken.balanceOf(investor), quoteAmount);
    }
}
