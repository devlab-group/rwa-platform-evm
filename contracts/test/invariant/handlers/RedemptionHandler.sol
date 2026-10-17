// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {RWAToken} from "../../../src/RWAToken.sol";
import {ComplianceRegistry} from "../../../src/ComplianceRegistry.sol";
import {SupplyController} from "../../../src/SupplyController.sol";
import {Vault} from "../../../src/Vault.sol";
import {RedemptionEscrow} from "../../../src/RedemptionEscrow.sol";
import {FixedPriceStrategy} from "../../../src/pricing/FixedPriceStrategy.sol";
import {MockERC20} from "../../mocks/token/MockERC20.sol";
import {ISupplyController} from "../../../src/interfaces/ISupplyController.sol";
import {IRedemptionEscrow} from "../../../src/interfaces/IRedemptionEscrow.sol";

/// @notice Fuzzes mint, buy, request, fund, claim, reject, cancel and burn with ghost accounting
/// @dev Reverts skip the step; ok_ counters track successful actions
contract RedemptionHandler is Test {
    RWAToken public token;
    ComplianceRegistry public compliance;
    SupplyController public supplyController;
    Vault public vault;
    RedemptionEscrow public escrow;
    FixedPriceStrategy public strategy;
    MockERC20 public quoteToken;

    address public auditor;
    uint256 public auditorPk;
    address public treasurer;
    address public redemptionManager;
    address public factory;
    bytes32 public complianceRole;

    address[] public actors;

    // ghost accounting
    uint256 public ghost_totalMinted;
    uint256 public ghost_totalBurned;
    uint256 public ghost_escrowedRwaSum; // RWA in Pending and Funded requests
    uint256 public ghost_fundedQuoteSum; // Quote in Funded requests
    uint256 public ghost_badClaims; // claims that misdirected RWA or quote
    uint256 public ghost_repeatCancels; // cancels that succeeded on an already cancelled request

    // calls made and calls that succeeded, per action
    uint256 public totalCalls;
    uint256 public ok_mint;
    uint256 public ok_burn;
    uint256 public ok_buy;
    uint256 public ok_request;
    uint256 public ok_fund;
    uint256 public ok_claim;
    uint256 public ok_reject;
    uint256 public ok_cancel;
    uint256 public ok_repeatCancelTried;

    uint256[] internal _pendingIds;
    uint256[] internal _fundedIds;
    uint256[] internal _cancelledIds;
    mapping(uint256 id => uint256 rwaAmount) internal _rwaAmountOf;
    mapping(uint256 id => uint256 quoteAmount) internal _quoteAmountOf;

    uint256 internal _nonce = 1;

    struct Deployment {
        RWAToken token;
        ComplianceRegistry compliance;
        SupplyController supplyController;
        Vault vault;
        RedemptionEscrow escrow;
        FixedPriceStrategy strategy;
        MockERC20 quoteToken;
        address auditor;
        uint256 auditorPk;
        address treasurer;
        address redemptionManager;
        address factory;
    }

    constructor(Deployment memory d, address[] memory actors_) {
        token = d.token;
        compliance = d.compliance;
        supplyController = d.supplyController;
        vault = d.vault;
        escrow = d.escrow;
        strategy = d.strategy;
        quoteToken = d.quoteToken;
        auditor = d.auditor;
        auditorPk = d.auditorPk;
        treasurer = d.treasurer;
        redemptionManager = d.redemptionManager;
        factory = d.factory;
        actors = actors_;
        complianceRole = compliance.COMPLIANCE_ROLE();
    }

    // ---- fuzzed actions: each counts its calls and its successes ----

    function mint(uint96 amountSeed) external {
        totalCalls++;
        _mint(bound(amountSeed, 1, 1_000_000 ether));
    }

    function burn(uint96 amountSeed) external {
        totalCalls++;
        uint256 vaultBalance = token.balanceOf(address(vault));
        if (vaultBalance == 0) return;
        uint256 amount = bound(amountSeed, 1, vaultBalance);
        ISupplyController.BurnAttestation memory b = ISupplyController.BurnAttestation({
            auditor: auditor,
            profileDigest: supplyController.profileDigest(),
            operationId: keccak256(abi.encodePacked("INV-OP-", _nonce)),
            metadataDigest: keccak256("invariant-metadata-burn"),
            amount: amount,
            nonce: _nextNonce(),
            validUntil: uint64(block.timestamp + 1 days),
            vault: address(vault)
        });
        bytes memory sig = _signBurn(b);
        try supplyController.burn(b, sig) {
            ghost_totalBurned += amount;
            ok_burn++;
        } catch {}
    }

    function buy(uint96 amountSeed, uint256 actorSeed) external {
        totalCalls++;
        uint256 inv = vault.inventory();
        if (inv == 0) return;
        _buy(_actor(actorSeed), bound(amountSeed, 1, inv));
    }

    function requestRedemption(uint96 amountSeed, uint256 actorSeed) external {
        totalCalls++;
        address actor = _actor(actorSeed);
        uint256 balance = token.balanceOf(actor);
        if (balance == 0) return;
        _request(actor, bound(amountSeed, 1, balance));
    }

    function fundRedemption(uint256 idSeed) external {
        totalCalls++;
        if (_pendingIds.length == 0) return;
        _fund(idSeed % _pendingIds.length);
    }

    function claimRedemption(uint256 idSeed) external {
        totalCalls++;
        if (_fundedIds.length == 0) return;
        _claim(idSeed % _fundedIds.length);
    }

    function rejectRedemption(uint256 idSeed) external {
        totalCalls++;
        if (_pendingIds.length == 0) return;
        uint256 index = idSeed % _pendingIds.length;
        uint256 id = _pendingIds[index];

        vm.prank(redemptionManager);
        try escrow.rejectRedemption(id, keccak256("invariant-reject")) {
            ghost_escrowedRwaSum -= _rwaAmountOf[id];
            _removePending(index);
            ok_reject++;
        } catch {}
    }

    /// @dev Advance by 0..timeout to test premature cancels while retaining Pending requests for other flows
    function cancelRedemption(uint256 idSeed, uint32 warpSeed) external {
        totalCalls++;
        if (_pendingIds.length == 0) return;
        uint256 index = idSeed % _pendingIds.length;
        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(_pendingIds[index]);

        vm.warp(block.timestamp + bound(warpSeed, 0, escrow.redemptionTimeout()));
        _cancel(index, r.beneficiary);
    }

    /// @dev Repeat cancellation must fail without draining RWA reserved for other requests
    function cancelAgain(uint256 idSeed) external {
        totalCalls++;
        if (_cancelledIds.length == 0) return;
        uint256 id = _cancelledIds[idSeed % _cancelledIds.length];
        address beneficiary = escrow.getRedemption(id).beneficiary;
        ok_repeatCancelTried++;
        vm.prank(beneficiary);
        try escrow.cancelRedemption(id) {
            ghost_repeatCancels++;
        } catch {}
    }

    /// @dev Sum of RWA held by the tracked holders: the actors, the Vault and the escrow
    function trackedRwaHoldings() external view returns (uint256 sum) {
        for (uint256 i; i < actors.length; ++i) {
            sum += token.balanceOf(actors[i]);
        }
        sum += token.balanceOf(address(vault)) + token.balanceOf(address(escrow));
    }

    /// @dev Mint/buy as needed to reach a selected flow in one call:
    ///      0 request/fund/claim; 1 leave Pending; 2 request/wait/cancel
    function redeemFlow(uint96 amountSeed, uint256 actorSeed, uint8 mode) external {
        totalCalls++;
        address actor = _actor(actorSeed);
        uint256 amount = bound(amountSeed, 1 ether, 1_000 ether);
        if (vault.inventory() < amount) _mint(amount);
        if (token.balanceOf(actor) < amount && !_buy(actor, amount)) return;

        uint256 pendingBefore = _pendingIds.length;
        if (!_request(actor, amount) || _pendingIds.length != pendingBefore + 1) return;
        uint256 index = _pendingIds.length - 1;
        mode = mode % 3;
        if (mode == 0) {
            if (_fund(index)) _claim(_fundedIds.length - 1);
        } else if (mode == 2) {
            vm.warp(block.timestamp + escrow.redemptionTimeout());
            _cancel(index, actor);
        }
    }

    // ---- shared steps ----

    function _mint(uint256 amount) internal {
        ISupplyController.MintAttestation memory a = ISupplyController.MintAttestation({
            auditor: auditor,
            profileDigest: supplyController.profileDigest(),
            recordKey: keccak256(abi.encodePacked("INV-REC-", _nonce)),
            metadataDigest: keccak256("invariant-metadata"),
            amount: amount,
            nonce: _nextNonce(),
            validUntil: uint64(block.timestamp + 1 days),
            vault: address(vault)
        });
        bytes memory sig = _signMint(a);
        try supplyController.mint(a, sig) {
            ghost_totalMinted += amount;
            ok_mint++;
        } catch {}
    }

    function _buy(address actor, uint256 amount) internal returns (bool ok) {
        uint256 quote = strategy.quotePurchase(amount);
        quoteToken.mint(actor, quote);
        vm.prank(actor);
        quoteToken.approve(address(vault), quote);

        vm.prank(actor);
        try vault.buy(amount, quote, actor, uint64(block.timestamp + 1 days)) {
            ok = true;
            ok_buy++;
        } catch {}
    }

    function _request(address actor, uint256 amount) internal returns (bool ok) {
        uint256 quote = strategy.quoteRedemption(amount);
        if (quote == 0) return false;

        vm.prank(actor);
        token.approve(address(escrow), amount);
        vm.prank(actor);
        try escrow.requestRedemption(amount, 0, uint64(block.timestamp + 1 days)) returns (uint256 id) {
            _pendingIds.push(id);
            _rwaAmountOf[id] = amount;
            _quoteAmountOf[id] = quote;
            ghost_escrowedRwaSum += amount;
            ok = true;
            ok_request++;
        } catch {}
    }

    function _fund(uint256 index) internal returns (bool ok) {
        uint256 id = _pendingIds[index];
        uint256 quote = _quoteAmountOf[id];

        quoteToken.mint(treasurer, quote);
        vm.prank(treasurer);
        quoteToken.approve(address(escrow), quote);

        vm.prank(treasurer);
        try escrow.fundRedemption(id) {
            _removePending(index);
            _fundedIds.push(id);
            ghost_fundedQuoteSum += quote;
            ok = true;
            ok_fund++;
        } catch {}
    }

    function _cancel(uint256 index, address beneficiary) internal {
        uint256 id = _pendingIds[index];
        vm.prank(beneficiary);
        try escrow.cancelRedemption(id) {
            ghost_escrowedRwaSum -= _rwaAmountOf[id];
            _removePending(index);
            _cancelledIds.push(id);
            ok_cancel++;
        } catch {}
    }

    /// @dev Check exact RWA return to the Vault and exact quote payment only to the beneficiary
    function _claim(uint256 index) internal {
        uint256 id = _fundedIds[index];
        address beneficiary = escrow.getRedemption(id).beneficiary;
        uint256 vaultRwaBefore = token.balanceOf(address(vault));
        uint256[] memory quoteBefore = new uint256[](actors.length);
        for (uint256 i; i < actors.length; ++i) {
            quoteBefore[i] = quoteToken.balanceOf(actors[i]);
        }
        uint256 treasurerQuoteBefore = quoteToken.balanceOf(treasurer);

        try escrow.claimRedemption(id) {
            ghost_escrowedRwaSum -= _rwaAmountOf[id];
            ghost_fundedQuoteSum -= _quoteAmountOf[id];
            _removeFunded(index);
            ok_claim++;

            if (token.balanceOf(address(vault)) - vaultRwaBefore != _rwaAmountOf[id]) ghost_badClaims++;
            if (quoteToken.balanceOf(treasurer) != treasurerQuoteBefore) ghost_badClaims++;
            for (uint256 i; i < actors.length; ++i) {
                uint256 expected = actors[i] == beneficiary ? _quoteAmountOf[id] : 0;
                if (quoteToken.balanceOf(actors[i]) - quoteBefore[i] != expected) ghost_badClaims++;
            }
        } catch {}
    }

    function openPendingCount() external view returns (uint256) {
        return _pendingIds.length;
    }

    function openFundedCount() external view returns (uint256) {
        return _fundedIds.length;
    }

    function _actor(uint256 seed) internal view returns (address) {
        return actors[seed % actors.length];
    }

    function _removePending(uint256 index) internal {
        _pendingIds[index] = _pendingIds[_pendingIds.length - 1];
        _pendingIds.pop();
    }

    function _removeFunded(uint256 index) internal {
        _fundedIds[index] = _fundedIds[_fundedIds.length - 1];
        _fundedIds.pop();
    }

    function _nextNonce() internal returns (uint256) {
        return _nonce++;
    }

    function _signMint(ISupplyController.MintAttestation memory a) internal view returns (bytes memory) {
        bytes32 structHash = keccak256(
            abi.encode(
                supplyController.MINT_ATTESTATION_TYPEHASH(),
                a.auditor,
                a.profileDigest,
                a.recordKey,
                a.metadataDigest,
                a.amount,
                a.nonce,
                a.validUntil,
                a.vault
            )
        );
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(auditorPk, _domainDigest(structHash));
        return abi.encodePacked(r, s, v);
    }

    function _signBurn(ISupplyController.BurnAttestation memory b) internal view returns (bytes memory) {
        bytes32 structHash = keccak256(
            abi.encode(
                supplyController.BURN_ATTESTATION_TYPEHASH(),
                b.auditor,
                b.profileDigest,
                b.operationId,
                b.metadataDigest,
                b.amount,
                b.nonce,
                b.validUntil,
                b.vault
            )
        );
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(auditorPk, _domainDigest(structHash));
        return abi.encodePacked(r, s, v);
    }

    function _domainDigest(bytes32 structHash) internal view returns (bytes32) {
        bytes32 domainSeparator = keccak256(
            abi.encode(
                keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"),
                keccak256(bytes("RWA-Supply-Attestation")),
                keccak256(bytes("1")),
                block.chainid,
                address(supplyController)
            )
        );
        return keccak256(abi.encodePacked(bytes2(0x1901), domainSeparator, structHash));
    }
}
