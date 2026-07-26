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

/// @notice Fuzzed sequences of mint/buy/request/fund/claim/reject/cancel/burn against one
///         deployed project, with ghost accounting the invariant test checks against live
///         contract state. All calls are wrapped so an expected revert (insufficient balance,
///         no open request of the right kind, etc.) just skips the step rather than aborting
///         the run — the handler explores whatever state is currently reachable.
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
    uint256 public ghost_escrowedRwaSum; // sum of rwaAmount over {Pending, Funded} requests
    uint256 public ghost_fundedQuoteSum; // sum of quoteAmount over {Funded} requests

    uint256[] internal _pendingIds;
    uint256[] internal _fundedIds;
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

    function mint(uint96 amountSeed) external {
        uint256 amount = bound(amountSeed, 1, 1_000_000 ether);
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
        } catch {}
    }

    function burn(uint96 amountSeed) external {
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
        } catch {}
    }

    function buy(uint96 amountSeed, uint256 actorSeed) external {
        address actor = _actor(actorSeed);
        uint256 inv = vault.inventory();
        if (inv == 0) return;
        uint256 amount = bound(amountSeed, 1, inv);
        uint256 quote = strategy.quotePurchase(amount);

        quoteToken.mint(actor, quote);
        vm.prank(actor);
        quoteToken.approve(address(vault), quote);

        vm.prank(actor);
        try vault.buy(amount, quote, actor, uint64(block.timestamp + 1 days)) {} catch {}
    }

    function requestRedemption(uint96 amountSeed, uint256 actorSeed) external {
        address actor = _actor(actorSeed);
        uint256 balance = token.balanceOf(actor);
        if (balance == 0) return;
        uint256 amount = bound(amountSeed, 1, balance);
        uint256 quote = strategy.quoteRedemption(amount);
        if (quote == 0) return;

        vm.prank(actor);
        token.approve(address(escrow), amount);
        vm.prank(actor);
        try escrow.requestRedemption(amount, 0, uint64(block.timestamp + 1 days)) returns (uint256 id) {
            _pendingIds.push(id);
            _rwaAmountOf[id] = amount;
            _quoteAmountOf[id] = quote;
            ghost_escrowedRwaSum += amount;
        } catch {}
    }

    function fundRedemption(uint256 idSeed) external {
        if (_pendingIds.length == 0) return;
        uint256 index = idSeed % _pendingIds.length;
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
        } catch {}
    }

    function claimRedemption(uint256 idSeed) external {
        if (_fundedIds.length == 0) return;
        uint256 index = idSeed % _fundedIds.length;
        uint256 id = _fundedIds[index];

        try escrow.claimRedemption(id) {
            ghost_escrowedRwaSum -= _rwaAmountOf[id];
            ghost_fundedQuoteSum -= _quoteAmountOf[id];
            _removeFunded(index);
        } catch {}
    }

    function rejectRedemption(uint256 idSeed) external {
        if (_pendingIds.length == 0) return;
        uint256 index = idSeed % _pendingIds.length;
        uint256 id = _pendingIds[index];

        vm.prank(redemptionManager);
        try escrow.rejectRedemption(id, keccak256("invariant-reject")) {
            ghost_escrowedRwaSum -= _rwaAmountOf[id];
            _removePending(index);
        } catch {}
    }

    function cancelRedemption(uint256 idSeed, uint32 warpSeed) external {
        if (_pendingIds.length == 0) return;
        uint256 index = idSeed % _pendingIds.length;
        uint256 id = _pendingIds[index];
        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(id);

        vm.warp(block.timestamp + escrow.redemptionTimeout() + bound(warpSeed, 0, 30 days));
        vm.prank(r.beneficiary);
        try escrow.cancelRedemption(id) {
            ghost_escrowedRwaSum -= _rwaAmountOf[id];
            _removePending(index);
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
