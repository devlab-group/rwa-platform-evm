// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {IAccessControlEnumerable} from "@openzeppelin/contracts/access/extensions/IAccessControlEnumerable.sol";
import {IERC165} from "@openzeppelin/contracts/utils/introspection/IERC165.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {IERC20Errors} from "@openzeppelin/contracts/interfaces/draft-IERC6093.sol";
import {Vm} from "forge-std/Vm.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {RWAToken} from "../../src/RWAToken.sol";
import {IRWAToken} from "../../src/interfaces/IRWAToken.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";
import {IERC7943} from "../../src/interfaces/IERC7943.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";

contract RWATokenTest is TestBase {
    function test_metadata() public view {
        assertEq(token.name(), "Gold Bar Token");
        assertEq(token.symbol(), "GBT");
        assertEq(token.decimals(), TOKEN_DECIMALS);
        assertEq(token.compliance(), address(compliance));
        assertEq(token.supplyController(), address(supplyController));
    }

    function test_onlySupplyControllerCanMint() public {
        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.OnlySupplyController.selector, outsider));
        token.controllerMint(investor, 1 ether);
    }

    function test_onlySupplyControllerCanBurn() public {
        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.OnlySupplyController.selector, outsider));
        token.controllerBurn(investor, 1 ether);
    }

    function test_setSupplyController_onlyOnce() public {
        vm.prank(address(factory));
        vm.expectRevert(IRWAToken.SupplyControllerAlreadySet.selector);
        token.setSupplyController(outsider);
    }

    function test_setSupplyController_onlyDeployer() public {
        // Deploy a fresh, unwired token to exercise the one-time setter directly.
        vm.prank(address(this));
        RWAToken fresh = new RWAToken("Fresh", "FRSH", 18, address(compliance), admin, 0);

        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.OnlyTokenDeployer.selector, outsider));
        fresh.setSupplyController(outsider);

        vm.prank(address(this));
        fresh.setSupplyController(outsider);
        assertEq(fresh.supplyController(), outsider);
    }

    function test_constructor_zeroComplianceReverts() public {
        vm.expectRevert(RWAToken.ZeroAddress.selector);
        new RWAToken("Fresh", "FRSH", 18, address(0), admin, 0);
    }

    function test_setSupplyController_zeroAddressReverts() public {
        RWAToken fresh = new RWAToken("Fresh", "FRSH", 18, address(compliance), admin, 0);
        vm.expectRevert(RWAToken.ZeroAddress.selector);
        fresh.setSupplyController(address(0));
    }

    function test_transfer_bothPartiesMustBeAllowed() public {
        _mintTo(investor, 100 ether);

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.RecipientNotAllowed.selector, outsider));
        token.transfer(outsider, 1 ether);
    }

    function test_transfer_senderNotAllowedReverts() public {
        _mintTo(investor, 100 ether);

        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.SenderNotAllowed.selector, investor));
        token.transfer(investor2, 1 ether);
    }

    function test_blockedHolderRetainsBalance() public {
        _mintTo(investor, 100 ether);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        assertEq(token.balanceOf(investor), 100 ether);
    }

    function test_transfer_succeedsWhenBothAllowed() public {
        _mintTo(investor, 100 ether);
        vm.prank(investor);
        token.transfer(investor2, 10 ether);
        assertEq(token.balanceOf(investor2), 10 ether);
    }

    function test_burnFromVaultSucceeds() public {
        // controllerBurn (to == 0) never re-checks compliance — only ordinary transfers
        // (from != 0 && to != 0) are gated — and the Vault can't be blocked
        // anyway (see ComplianceRegistryTest for that regression coverage), so this is now a
        // plain happy-path sanity check rather than a compliance-bypass proof.
        _mintTo(address(vault), 5 ether);

        ISupplyController.BurnAttestation memory b = _burnAttestation(auditor, 5 ether, keccak256("OP-1"), 2);
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        supplyController.burn(b, sig);
        assertEq(token.balanceOf(address(vault)), 0);
    }

    function test_pause_blocksTransfer() public {
        _mintTo(investor, 10 ether);
        vm.prank(admin);
        token.pause();
        assertTrue(token.paused());

        vm.prank(investor);
        vm.expectRevert();
        token.transfer(investor2, 1 ether);
    }

    function test_unpause_restoresTransfer() public {
        _mintTo(investor, 10 ether);
        vm.prank(admin);
        token.pause();
        vm.prank(admin);
        token.unpause();

        vm.prank(investor);
        token.transfer(investor2, 1 ether);
        assertEq(token.balanceOf(investor2), 1 ether);
    }

    // ---- setRedemptionEscrow / returnEscrowedRWA (escrow-only pause bypass) ----

    function test_setRedemptionEscrow_onlyOnce() public {
        vm.prank(address(factory));
        vm.expectRevert(RWAToken.RedemptionEscrowAlreadySet.selector);
        token.setRedemptionEscrow(outsider);
    }

    function test_setRedemptionEscrow_onlyDeployer() public {
        // Deploy a fresh, unwired token to exercise the one-time setter directly.
        vm.prank(address(this));
        RWAToken fresh = new RWAToken("Fresh", "FRSH", 18, address(compliance), admin, 0);

        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.OnlyTokenDeployer.selector, outsider));
        fresh.setRedemptionEscrow(outsider);

        vm.prank(address(this));
        fresh.setRedemptionEscrow(outsider);
        assertEq(fresh.redemptionEscrow(), outsider);
    }

    function test_setRedemptionEscrow_zeroAddressReverts() public {
        RWAToken fresh = new RWAToken("Fresh", "FRSH", 18, address(compliance), admin, 0);
        vm.expectRevert(RWAToken.ZeroAddress.selector);
        fresh.setRedemptionEscrow(address(0));
    }

    function test_returnEscrowedRWA_onlyRedemptionEscrow() public {
        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.OnlyRedemptionEscrow.selector, outsider));
        token.returnEscrowedRWA(investor, 1 ether);
    }

    function test_returnEscrowedRWA_recipientMustBeAllowed() public {
        // Give the (pinned-Allowed) escrow a balance to move, simulating an outstanding
        // redemption request, without going through the full requestRedemption flow.
        _mintTo(address(escrow), 5 ether);

        vm.prank(address(escrow));
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.RecipientNotAllowed.selector, outsider));
        token.returnEscrowedRWA(outsider, 1 ether);
    }

    function test_returnEscrowedRWA_movesEscrowBalanceToRecipient_evenWhilePaused() public {
        _mintTo(address(escrow), 5 ether);
        vm.prank(admin);
        token.pause();

        vm.prank(address(escrow));
        token.returnEscrowedRWA(investor, 5 ether);

        assertEq(token.balanceOf(investor), 5 ether);
        assertEq(token.balanceOf(address(escrow)), 0);
        assertTrue(token.paused(), "pause must remain in effect for everything else");
    }

    function test_pause_onlyPauserRole() public {
        bytes32 pauserRole = token.PAUSER_ROLE();
        vm.prank(outsider);
        vm.expectRevert(
            abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, outsider, pauserRole)
        );
        token.pause();
    }

    // ---- ERC-7943 (uRWA) queries ----

    function test_canSendReceive_followTheComplianceRegistry() public {
        assertTrue(token.canSend(investor));
        assertTrue(token.canReceive(investor));
        assertTrue(token.canTransfer(investor, investor2, 1 ether));

        // Unknown wallet: never given a record at all.
        assertFalse(token.canSend(outsider));
        assertFalse(token.canReceive(outsider));
        assertFalse(token.canTransfer(outsider, investor, 1 ether));
        assertFalse(token.canTransfer(investor, outsider, 1 ether));
    }

    function test_canSendReceive_blockedWallet() public {
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);

        assertFalse(token.canSend(investor));
        assertFalse(token.canReceive(investor));
        assertFalse(token.canTransfer(investor, investor2, 1 ether));
        assertFalse(token.canTransfer(investor2, investor, 1 ether));
    }

    function test_canSendReceive_expiredWallet() public {
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed, uint64(block.timestamp + 1 days));
        assertTrue(token.canSend(investor));

        vm.warp(block.timestamp + 2 days);
        assertFalse(token.canSend(investor));
        assertFalse(token.canReceive(investor));
        assertFalse(token.canTransfer(investor, investor2, 1 ether));
        assertFalse(token.canTransfer(investor2, investor, 1 ether));
    }

    function test_canTransfer_falseWhilePausedButAccountChecksUnchanged() public {
        vm.prank(admin);
        token.pause();

        assertTrue(token.canSend(investor), "pause is not an account-eligibility statement");
        assertTrue(token.canReceive(investor2));
        assertFalse(token.canTransfer(investor, investor2, 1 ether));
    }

    function test_canTransfer_unfrozenAmounts() public {
        _mintTo(investor, 100 ether);
        assertEq(token.getFrozenTokens(investor), 0, "nothing is frozen by default");
        assertTrue(token.canTransfer(investor, investor2, 100 ether));

        _freeze(investor, 40 ether);
        assertEq(token.getFrozenTokens(investor), 40 ether);
        assertTrue(token.canTransfer(investor, investor2, 60 ether), "the unfrozen part still moves");
        assertFalse(token.canTransfer(investor, investor2, 61 ether), "one unit into the frozen part");
        assertFalse(token.canTransfer(investor, investor2, 100 ether));
    }

    function test_canTransfer_fullyFrozen() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, 100 ether);

        assertTrue(token.canTransfer(investor, investor2, 0), "a zero-value transfer takes nothing");
        assertFalse(token.canTransfer(investor, investor2, 1));
        assertFalse(token.canTransfer(investor, investor2, 100 ether));
    }

    function test_canTransfer_frozenAboveBalanceDoesNotUnderflow() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, type(uint256).max);

        assertFalse(token.canTransfer(investor, investor2, 1));
        assertFalse(token.canTransfer(investor, investor2, 100 ether));
    }

    /// @dev An amount nobody has is an ERC-20 problem, not a permissioned refusal, so the
    ///      standard query must not answer it.
    function test_canTransfer_overBalanceIsNotAPermissionedRefusal() public {
        _mintTo(investor, 100 ether);
        assertTrue(token.canTransfer(investor, investor2, 101 ether));
        assertTrue(token.canTransfer(investor2, investor, 1 ether), "investor2 holds nothing at all");

        vm.prank(investor);
        vm.expectRevert();
        token.transfer(investor2, 101 ether);
    }

    /// @dev `staticcall` proves it: any storage write in these paths would revert the call.
    function test_queries_areSideEffectFree() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, 40 ether);

        _assertStaticCallSucceeds(abi.encodeCall(IERC7943.canSend, (investor)));
        _assertStaticCallSucceeds(abi.encodeCall(IERC7943.canReceive, (investor)));
        _assertStaticCallSucceeds(abi.encodeCall(IERC7943.getFrozenTokens, (investor)));
        _assertStaticCallSucceeds(abi.encodeCall(IERC7943.canTransfer, (investor, investor2, 50 ether)));
    }

    function test_supportsInterface_erc7943() public view {
        assertEq(type(IERC7943).interfaceId, bytes4(0x3edbb4c4), "final uRWA fungible interface id");
        assertTrue(token.supportsInterface(type(IERC7943).interfaceId));

        // The inherited introspection must survive the added branch.
        assertTrue(token.supportsInterface(type(IERC165).interfaceId));
        assertTrue(token.supportsInterface(type(IAccessControl).interfaceId));
        assertTrue(token.supportsInterface(type(IAccessControlEnumerable).interfaceId));
        assertFalse(token.supportsInterface(bytes4(0xdeadbeef)));
    }

    function testFuzz_canTransfer_neverReverts(address from, address to, uint256 amount) public view {
        token.canTransfer(from, to, amount);
    }

    function testFuzz_canTransfer_falseExactlyWhenTheAmountReachesFrozenTokens(uint256 frozen, uint256 amount) public {
        uint256 balance = 100 ether;
        _mintTo(investor, balance);
        _freeze(investor, bound(frozen, 0, type(uint256).max));
        frozen = token.getFrozenTokens(investor);
        amount = bound(amount, 0, balance);

        uint256 unfrozen = balance > frozen ? balance - frozen : 0;
        assertEq(token.canTransfer(investor, investor2, amount), amount <= unfrozen);
    }

    // ---- ERC-7943 (uRWA) freeze authority ----

    function test_setFrozenTokens_onlyDefaultAdmin() public {
        _expectUnauthorized(outsider);
        vm.prank(outsider);
        token.setFrozenTokens(investor, 1 ether);

        address pauser = _pauserOnly();
        _expectUnauthorized(pauser);
        vm.prank(pauser);
        token.setFrozenTokens(investor, 1 ether);

        assertEq(token.getFrozenTokens(investor), 0);
    }

    function test_forcedTransfer_onlyDefaultAdmin() public {
        _mintTo(investor, 10 ether);

        _expectUnauthorized(outsider);
        vm.prank(outsider);
        token.forcedTransfer(investor, investor2, 1 ether);

        address pauser = _pauserOnly();
        _expectUnauthorized(pauser);
        vm.prank(pauser);
        token.forcedTransfer(investor, investor2, 1 ether);

        assertEq(token.balanceOf(investor2), 0);
    }

    function test_enforcementFollowsTheDefaultAdminRotation() public {
        _mintTo(investor, 10 ether);
        address newAdmin = makeAddr("newAdmin");
        _rotateAdminTo(newAdmin);

        _expectUnauthorized(admin);
        vm.prank(admin);
        token.setFrozenTokens(investor, 1 ether);

        _expectUnauthorized(admin);
        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 1 ether);

        vm.prank(newAdmin);
        token.setFrozenTokens(investor, 1 ether);
        assertEq(token.getFrozenTokens(investor), 1 ether);

        vm.prank(newAdmin);
        token.forcedTransfer(investor, investor2, 1 ether);
        assertEq(token.balanceOf(investor2), 1 ether);
    }

    // ---- setFrozenTokens semantics ----

    function test_setFrozenTokens_overwritesAndReleases() public {
        _mintTo(investor, 100 ether);

        vm.expectEmit(true, false, false, true, address(token));
        emit IERC7943.Frozen(investor, 40 ether);
        _freeze(investor, 40 ether);
        assertEq(token.getFrozenTokens(investor), 40 ether);

        // Absolute, not additive: 10 replaces 40 instead of totalling 50.
        _freeze(investor, 10 ether);
        assertEq(token.getFrozenTokens(investor), 10 ether);

        vm.expectEmit(true, false, false, true, address(token));
        emit IERC7943.Frozen(investor, 0);
        _freeze(investor, 0);
        assertEq(token.getFrozenTokens(investor), 0);
        assertTrue(token.canTransfer(investor, investor2, 100 ether));
    }

    function test_setFrozenTokens_aboveBalanceWithholdsIncomingTokens() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, 150 ether);
        assertFalse(token.canTransfer(investor, investor2, 1));

        // Topping the balance up past the frozen amount frees the difference, nothing more.
        _mintTo(investor, 60 ether, "REC-2", 2);
        assertEq(token.balanceOf(investor), 160 ether);
        assertTrue(token.canTransfer(investor, investor2, 10 ether));
        assertFalse(token.canTransfer(investor, investor2, 11 ether));

        vm.prank(investor);
        token.transfer(investor2, 10 ether);
        assertEq(token.balanceOf(investor), 150 ether);
    }

    function test_setFrozenTokens_rejectsZeroAccount() public {
        vm.prank(admin);
        vm.expectRevert(RWAToken.ZeroAddress.selector);
        token.setFrozenTokens(address(0), 1);
    }

    function test_setFrozenTokens_rejectsSystemAddresses() public {
        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.SystemAddressCannotBeFrozen.selector, address(vault)));
        token.setFrozenTokens(address(vault), 1);

        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.SystemAddressCannotBeFrozen.selector, address(escrow)));
        token.setFrozenTokens(address(escrow), 1);

        // Zero stays callable so a system address can always be walked back to a clean state.
        vm.prank(admin);
        token.setFrozenTokens(address(vault), 0);
        vm.prank(admin);
        token.setFrozenTokens(address(escrow), 0);
        assertEq(token.getFrozenTokens(address(vault)), 0);
        assertEq(token.getFrozenTokens(address(escrow)), 0);
    }

    /// @dev The escrow's pause bypass moves its own balance, so an unfreezable escrow is what
    ///      keeps a redemption cancel or claim from ever being trapped by a freeze.
    function test_returnEscrowedRWA_cannotBeFrozenAtTheSystemSender() public {
        _mintTo(address(escrow), 5 ether);

        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.SystemAddressCannotBeFrozen.selector, address(escrow)));
        token.setFrozenTokens(address(escrow), 5 ether);

        vm.prank(admin);
        token.pause();
        vm.prank(address(escrow));
        token.returnEscrowedRWA(investor, 5 ether);
        assertEq(token.balanceOf(investor), 5 ether);
    }

    // ---- normal transfer enforcement ----

    function test_transfer_stopsExactlyAtTheUnfrozenAmount() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, 40 ether);

        vm.prank(investor);
        vm.expectRevert(
            abi.encodeWithSelector(
                IERC7943.ERC7943InsufficientUnfrozenBalance.selector, investor, 60 ether + 1, 60 ether
            )
        );
        token.transfer(investor2, 60 ether + 1);

        vm.prank(investor);
        token.transfer(investor2, 60 ether);
        assertEq(token.balanceOf(investor2), 60 ether);
        assertEq(token.balanceOf(investor), 40 ether, "the frozen part stays put");
    }

    function test_transferFrom_obeysTheSameFrozenLimit() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, 40 ether);

        vm.prank(investor);
        token.approve(investor2, type(uint256).max);

        vm.prank(investor2);
        vm.expectRevert(
            abi.encodeWithSelector(IERC7943.ERC7943InsufficientUnfrozenBalance.selector, investor, 61 ether, 60 ether)
        );
        token.transferFrom(investor, investor2, 61 ether);

        vm.prank(investor2);
        token.transferFrom(investor, investor2, 60 ether);
        assertEq(token.balanceOf(investor2), 60 ether);
    }

    function test_transfer_overBalanceKeepsTheErc20Error() public {
        _mintTo(investor, 100 ether);

        vm.prank(investor);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientBalance.selector, investor, 100 ether, 101 ether)
        );
        token.transfer(investor2, 101 ether);
    }

    function test_transfer_pausedStaysImpossibleRegardlessOfFreeze() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, 0);
        vm.prank(admin);
        token.pause();

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(Pausable.EnforcedPause.selector));
        token.transfer(investor2, 1 ether);
    }

    // ---- forcedTransfer ----

    function test_forcedTransfer_movesTokensAndLeavesSupplyAlone() public {
        _mintTo(investor, 100 ether);
        uint256 supply = token.totalSupply();

        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 30 ether);

        assertEq(token.balanceOf(investor), 70 ether);
        assertEq(token.balanceOf(investor2), 30 ether);
        assertEq(token.totalSupply(), supply, "seizure is a move, never a mint or burn");
    }

    function test_forcedTransfer_worksOnBlockedAndExpiredSenders() public {
        _mintTo(investor, 100 ether);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);

        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 10 ether);
        assertEq(token.balanceOf(investor2), 10 ether);

        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed, uint64(block.timestamp + 1 days));
        vm.warp(block.timestamp + 2 days);
        assertFalse(token.canSend(investor));

        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 10 ether);
        assertEq(token.balanceOf(investor2), 20 ether);
    }

    function test_forcedTransfer_recipientMustStayCompliant() public {
        _mintTo(investor, 100 ether);

        // Unknown wallet.
        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.RecipientNotAllowed.selector, outsider));
        token.forcedTransfer(investor, outsider, 1 ether);

        // Blocked wallet.
        vm.prank(complianceOperator);
        compliance.setStatus(investor2, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.RecipientNotAllowed.selector, investor2));
        token.forcedTransfer(investor, investor2, 1 ether);

        // Expired record.
        vm.prank(complianceOperator);
        compliance.setStatus(investor2, IComplianceRegistry.ComplianceStatus.Allowed, uint64(block.timestamp + 1 days));
        vm.warp(block.timestamp + 2 days);
        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.RecipientNotAllowed.selector, investor2));
        token.forcedTransfer(investor, investor2, 1 ether);
    }

    function test_forcedTransfer_worksWhilePaused() public {
        _mintTo(investor, 100 ether);
        vm.prank(admin);
        token.pause();

        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 10 ether);

        assertEq(token.balanceOf(investor2), 10 ether);
        assertTrue(token.paused(), "the pause stays in effect for everyone else");
    }

    function test_forcedTransfer_rejectsZeroAddressesAndSelf() public {
        _mintTo(investor, 100 ether);

        vm.prank(admin);
        vm.expectRevert(RWAToken.ZeroAddress.selector);
        token.forcedTransfer(address(0), investor2, 1 ether);

        vm.prank(admin);
        vm.expectRevert(RWAToken.ZeroAddress.selector);
        token.forcedTransfer(investor, address(0), 1 ether);

        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.ForcedTransferToSelf.selector, investor));
        token.forcedTransfer(investor, investor, 1 ether);
    }

    function test_forcedTransfer_overBalanceReverts() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, 40 ether);

        vm.prank(admin);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientBalance.selector, investor, 100 ether, 101 ether)
        );
        token.forcedTransfer(investor, investor2, 101 ether);

        assertEq(token.getFrozenTokens(investor), 40 ether, "a reverted seizure leaves the freeze intact");
    }

    // ---- forced transfer against frozen tokens ----

    function test_forcedTransfer_withinUnfrozenLeavesTheFreezeAlone() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, 40 ether);

        vm.recordLogs();
        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 60 ether);

        assertEq(token.getFrozenTokens(investor), 40 ether);
        Vm.Log[] memory logs = vm.getRecordedLogs();
        assertEq(logs.length, 2, "no Frozen event when nothing frozen is touched");
        assertEq(logs[0].topics[0], keccak256("Transfer(address,address,uint256)"));
        assertEq(logs[1].topics[0], keccak256("ForcedTransfer(address,address,uint256)"));
    }

    function test_forcedTransfer_crossingIntoFrozenReducesItFirst() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, 40 ether);

        // 70 = 60 unfrozen + 10 taken out of the frozen 40.
        vm.recordLogs();
        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 70 ether);

        assertEq(token.getFrozenTokens(investor), 30 ether);
        assertEq(token.balanceOf(investor), 30 ether);

        Vm.Log[] memory logs = vm.getRecordedLogs();
        assertEq(logs.length, 3);
        assertEq(logs[0].topics[0], keccak256("Frozen(address,uint256)"), "Frozen comes first");
        assertEq(logs[1].topics[0], keccak256("Transfer(address,address,uint256)"));
        assertEq(logs[2].topics[0], keccak256("ForcedTransfer(address,address,uint256)"));
        assertEq(abi.decode(logs[0].data, (uint256)), 30 ether);
    }

    function test_forcedTransfer_fullyFrozenAccount() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, 100 ether);

        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 25 ether);

        assertEq(token.getFrozenTokens(investor), 75 ether, "frozen drops by exactly the amount taken");
        assertEq(token.balanceOf(investor), 75 ether);
        assertFalse(token.canTransfer(investor, investor2, 1), "the remainder is still fully frozen");
    }

    function test_forcedTransfer_frozenAboveBalanceReducesSafely() public {
        _mintTo(investor, 100 ether);
        _freeze(investor, 150 ether);

        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 100 ether);

        assertEq(token.getFrozenTokens(investor), 50 ether);
        assertEq(token.balanceOf(investor), 0);
    }

    // ---- supply invariants around the new authority ----

    function test_forcedTransfer_isNotASupplyPath() public {
        _mintTo(investor, 100 ether);
        uint256 supply = token.totalSupply();

        // The only mint/burn entry point is still the SupplyController.
        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.OnlySupplyController.selector, admin));
        token.controllerMint(investor, 1 ether);

        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.OnlySupplyController.selector, admin));
        token.controllerBurn(investor, 1 ether);

        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 100 ether);
        assertEq(token.totalSupply(), supply);
    }

    function testFuzz_forcedTransfer_neverChangesTotalSupply(uint256 frozen, uint256 amount) public {
        uint256 balance = 100 ether;
        _mintTo(investor, balance);
        _freeze(investor, frozen);
        amount = bound(amount, 0, balance);
        uint256 supply = token.totalSupply();

        vm.prank(admin);
        token.forcedTransfer(investor, investor2, amount);

        assertEq(token.totalSupply(), supply);
        assertEq(token.balanceOf(investor) + token.balanceOf(investor2), balance);
        assertLe(token.getFrozenTokens(investor), frozen, "frozen only ever goes down here");
    }

    function testFuzz_forcedTransfer_toSelfAlwaysReverts(uint256 amount) public {
        _mintTo(investor, 100 ether);
        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.ForcedTransferToSelf.selector, investor));
        token.forcedTransfer(investor, investor, amount);
    }

    function testFuzz_setFrozenTokens_acceptsAnyAmountAndNeverBreaksQueries(uint256 frozen, uint256 amount) public {
        uint256 balance = 100 ether;
        _mintTo(investor, balance);
        _freeze(investor, frozen);
        assertEq(token.getFrozenTokens(investor), frozen);

        uint256 unfrozen = balance > frozen ? balance - frozen : 0;
        amount = bound(amount, 0, balance);
        assertEq(token.canTransfer(investor, investor2, amount), amount <= unfrozen);

        if (amount > unfrozen) {
            vm.prank(investor);
            vm.expectRevert(
                abi.encodeWithSelector(IERC7943.ERC7943InsufficientUnfrozenBalance.selector, investor, amount, unfrozen)
            );
            token.transfer(investor2, amount);
        } else {
            vm.prank(investor);
            token.transfer(investor2, amount);
            assertEq(token.balanceOf(investor), balance - amount);
            assertGe(token.balanceOf(investor), frozen > balance ? 0 : frozen, "the frozen part never leaves");
        }
    }

    function testFuzz_setFrozenTokens_systemAddressesNeverEndUpFrozen(uint256 amount) public {
        address[2] memory systemAddresses = [address(vault), address(escrow)];
        for (uint256 i = 0; i < systemAddresses.length; i++) {
            address system = systemAddresses[i];
            vm.prank(admin);
            if (amount > 0) {
                vm.expectRevert(abi.encodeWithSelector(RWAToken.SystemAddressCannotBeFrozen.selector, system));
            }
            token.setFrozenTokens(system, amount);
            assertEq(token.getFrozenTokens(system), 0);
        }
    }

    function _freeze(address account, uint256 amount) internal {
        vm.prank(admin);
        token.setFrozenTokens(account, amount);
    }

    /// @dev A wallet holding PAUSER_ROLE and nothing else, to prove the emergency switch never
    ///      carries enforcement powers with it.
    function _pauserOnly() internal returns (address account) {
        account = makeAddr("pauserOnly");
        bytes32 pauserRole = token.PAUSER_ROLE();
        vm.prank(admin);
        token.grantRole(pauserRole, account);
    }

    function _rotateAdminTo(address newAdmin) internal {
        vm.prank(admin);
        token.beginDefaultAdminTransfer(newAdmin);
        vm.warp(block.timestamp + 1);
        vm.prank(newAdmin);
        token.acceptDefaultAdminTransfer();
    }

    function _expectUnauthorized(address account) internal {
        vm.expectRevert(
            abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, account, bytes32(0))
        );
    }

    function _assertStaticCallSucceeds(bytes memory call) internal view {
        (bool ok,) = address(token).staticcall(call);
        assertTrue(ok, "query wrote state");
    }

    function _mintTo(address to, uint256 amount) internal {
        _mintTo(to, amount, "REC-1", 1);
    }

    function _mintTo(address to, uint256 amount, string memory recordId, uint256 nonce) internal {
        if (amount == 0) return;
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, amount, recordId, nonce);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        supplyController.mint(a, sig);
        if (to != address(vault)) {
            vm.prank(address(vault));
            token.transfer(to, amount);
        }
    }
}
