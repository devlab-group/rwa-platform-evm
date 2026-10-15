// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {console2} from "forge-std/console2.sol";
import {DeployBase} from "./Deploy.s.sol";
import {IRWAFactory} from "../src/interfaces/IRWAFactory.sol";
import {ISupplyController} from "../src/interfaces/ISupplyController.sol";
import {IComplianceRegistry} from "../src/interfaces/IComplianceRegistry.sol";
import {RWAToken} from "../src/RWAToken.sol";
import {ComplianceRegistry} from "../src/ComplianceRegistry.sol";
import {SupplyController} from "../src/SupplyController.sol";
import {Vault} from "../src/Vault.sol";
import {RedemptionEscrow} from "../src/RedemptionEscrow.sol";
import {IncomeDistributor} from "../src/IncomeDistributor.sol";
import {FixedPriceStrategy} from "../src/pricing/FixedPriceStrategy.sol";
import {MockERC20} from "../test/mocks/token/MockERC20.sol";

/// @notice Broadcasts deploy -> allow -> mint (locally signed) -> buy -> request -> fund ->
///         claim -> burn of returned Vault inventory. Separate calls reuse the deployment
///         for income distribution/claim and redemption timeout/cancellation.
///
/// Usage: forge script script/E2EFlow.s.sol --broadcast --rpc-url http://localhost:8545 -vv
///
/// @dev Inherits deployment helpers from DeployBase; later calls reuse the logged addresses.
///      Shared step data uses state to avoid stack-too-deep without --via-ir.
contract E2EFlow is DeployBase {
    // Anvil's default account 1, used as the investor.
    uint256 internal constant ANVIL_ACCT1_PK = 0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d;

    uint256 internal _nonce = 1;

    uint256 internal _deployerPk;
    uint256 internal _investorPk;
    address internal _deployer; // admin/auditor/complianceOperator/treasurer/redemptionManager/...
    address internal _investor;

    RWAToken internal _token;
    ComplianceRegistry internal _compliance;
    SupplyController internal _supplyController;
    Vault internal _vault;
    RedemptionEscrow internal _escrow;
    FixedPriceStrategy internal _strategy;
    IncomeDistributor internal _income;
    MockERC20 internal _quoteToken;

    /// @notice The full flow: deploy -> allow -> mint -> buy -> request -> fund -> claim
    ///         -> burn. Ends with a distinct "E2E OK" log marker e2e.sh greps for.
    function run() external {
        _deploy();
        _allowInvestor();
        _mint();
        _buy();
        uint256 requestId = _requestRedemption();
        _fundAndClaim(requestId);
        _burnReturnedInventory();
        console2.log("E2E OK");
    }

    /// @notice Creates a Pending request on the existing deployment for the optional timeout/cancel branch
    /// @dev Run separately so e2e.sh can advance time with cast rpc before cancelIt
    function requestForCancelDemo() external returns (uint256 id) {
        _wireExisting();

        uint256 amount = 5 ether;
        uint256 quote = _strategy.quoteRedemption(amount);

        vm.startBroadcast(_investorPk);
        _token.approve(address(_escrow), amount);
        id = _escrow.requestRedemption(amount, quote, uint64(block.timestamp + 1 hours));
        vm.stopBroadcast();
        console2.log("Requested (for cancel demo) redemption id", id);
    }

    /// @notice Nice-to-have branch, step 2/2: cancel the request created by
    ///         `requestForCancelDemo`, after e2e.sh has advanced the live chain's clock past
    ///         `redemptionTimeout` via `cast rpc evm_increaseTime` + `evm_mine`.
    function cancelIt(uint256 id) external {
        _wireExisting();

        vm.startBroadcast(_investorPk);
        _escrow.cancelRedemption(id);
        vm.stopBroadcast();
        console2.log("Cancelled timed-out redemption id", id);
    }

    function _deploy() internal {
        (IRWAFactory.Deployment memory dep,, address quoteTokenAddr) = deployAll();
        _wireKeys();
        _wireDeployment(dep, quoteTokenAddr);
    }

    /// @dev Loads run()'s deployment from TOKEN, COMPLIANCE, SUPPLY_CONTROLLER, VAULT,
    ///      ESCROW, STRATEGY, INCOME_DISTRIBUTOR and QUOTE_TOKEN for the later entrypoints
    function _wireExisting() internal {
        _wireKeys();
        IRWAFactory.Deployment memory dep = IRWAFactory.Deployment({
            token: vm.envAddress("TOKEN"),
            compliance: vm.envAddress("COMPLIANCE"),
            supplyController: vm.envAddress("SUPPLY_CONTROLLER"),
            vault: vm.envAddress("VAULT"),
            redemptionEscrow: vm.envAddress("ESCROW"),
            strategy: vm.envAddress("STRATEGY"),
            incomeDistributor: vm.envAddress("INCOME_DISTRIBUTOR")
        });
        _wireDeployment(dep, vm.envAddress("QUOTE_TOKEN"));
    }

    function _wireKeys() internal {
        _deployerPk = _deployerPrivateKey();
        _investorPk = _investorPrivateKey();
        _deployer = vm.addr(_deployerPk);
        _investor = vm.addr(_investorPk);
    }

    /// @dev The public Anvil account 1 key is a fallback only on ANVIL_CHAIN_ID
    function _investorPrivateKey() internal view returns (uint256) {
        if (block.chainid == ANVIL_CHAIN_ID) {
            return vm.envOr("INVESTOR_PK", ANVIL_ACCT1_PK);
        }
        try vm.envUint("INVESTOR_PK") returns (uint256 pk) {
            return pk;
        } catch {
            revert RefusingAnvilKeyFallbackOnNonAnvilChain(block.chainid);
        }
    }

    function _wireDeployment(IRWAFactory.Deployment memory dep, address quoteTokenAddr) internal {
        _token = RWAToken(dep.token);
        _compliance = ComplianceRegistry(dep.compliance);
        _supplyController = SupplyController(dep.supplyController);
        _vault = Vault(dep.vault);
        _escrow = RedemptionEscrow(dep.redemptionEscrow);
        _strategy = FixedPriceStrategy(dep.strategy);
        _income = IncomeDistributor(dep.incomeDistributor);
        _quoteToken = MockERC20(quoteTokenAddr);
    }

    function _allowInvestor() internal {
        vm.startBroadcast(_deployerPk);
        _compliance.setStatus(_investor, IComplianceRegistry.ComplianceStatus.Allowed, 0);
        vm.stopBroadcast();
        console2.log("Allowed investor", _investor);
    }

    function _mint() internal {
        uint256 amount = 1_000 ether;
        ISupplyController.MintAttestation memory a = ISupplyController.MintAttestation({
            auditor: _deployer,
            profileDigest: _supplyController.profileDigest(),
            recordKey: keccak256(bytes("E2E-REC-1")),
            metadataDigest: keccak256("e2e-metadata-1"),
            amount: amount,
            nonce: _nextNonce(),
            validUntil: uint64(block.timestamp + 1 days),
            vault: address(_vault)
        });
        bytes memory sig = _signMint(a, _deployerPk);

        vm.startBroadcast(_deployerPk);
        _supplyController.mint(a, sig);
        vm.stopBroadcast();
        console2.log("Minted", amount, "to vault");
    }

    function _buy() internal {
        uint256 amount = 100 ether;
        uint256 quote = _strategy.quotePurchase(amount);

        vm.startBroadcast(_deployerPk);
        _quoteToken.mint(_investor, quote);
        vm.stopBroadcast();

        vm.startBroadcast(_investorPk);
        _quoteToken.approve(address(_vault), quote);
        _vault.buy(amount, quote, _investor, uint64(block.timestamp + 1 hours));
        vm.stopBroadcast();
        console2.log("Bought", amount, "for quote", quote);
    }

    function _requestRedemption() internal returns (uint256 id) {
        uint256 amount = 40 ether;
        uint256 quote = _strategy.quoteRedemption(amount);

        vm.startBroadcast(_investorPk);
        _token.approve(address(_escrow), amount);
        id = _escrow.requestRedemption(amount, quote, uint64(block.timestamp + 1 hours));
        vm.stopBroadcast();
        console2.log("Requested redemption id", id, "for quote", quote);
    }

    function _fundAndClaim(uint256 id) internal {
        uint256 quote = _escrow.getRedemption(id).quoteAmount;

        vm.startBroadcast(_deployerPk);
        _quoteToken.mint(_deployer, quote);
        _quoteToken.approve(address(_escrow), quote);
        _escrow.fundRedemption(id);
        vm.stopBroadcast();
        console2.log("Funded redemption id", id);

        vm.startBroadcast(_deployerPk);
        _escrow.claimRedemption(id);
        vm.stopBroadcast();
        console2.log("Claimed redemption id", id, "- status Completed");
    }

    /// @notice Funds and claims a single-leaf distribution for the investor (root = leaf, empty proof)
    /// @dev Reuses run()'s deployment in a separate invocation so the chosen record block
    ///      predates simulation and mining of createDistribution
    function distributeIncome() external {
        _wireExisting();
        uint256 amount = 1_000_000;
        bytes32 root = keccak256(bytes.concat(keccak256(abi.encode(_investor, amount))));

        uint64 recordBlock = uint64(block.number - 1);
        // Leave time for mining after simulation so the minimum claim window still holds
        uint64 deadline = uint64(block.timestamp) + _income.MIN_CLAIM_WINDOW() + 1 days;

        vm.startBroadcast(_deployerPk);
        _quoteToken.mint(_deployer, amount);
        _quoteToken.approve(address(_income), amount);
        uint256 id = _income.createDistribution(
            root, amount, recordBlock, deadline, "ipfs://e2e-income", keccak256("e2e-income")
        );
        vm.stopBroadcast();

        uint256 before = _quoteToken.balanceOf(_investor);
        vm.startBroadcast(_investorPk);
        _income.claim(id, amount, new bytes32[](0));
        vm.stopBroadcast();
        require(_quoteToken.balanceOf(_investor) - before == amount, "income claim balance");
        require(_income.isClaimed(id, _investor), "income not claimed");
        console2.log("Distributed and claimed income id", id, "amount", amount);
    }

    function _burnReturnedInventory() internal {
        uint256 amount = 40 ether; // exactly what claim() returned to the Vault
        ISupplyController.BurnAttestation memory b = ISupplyController.BurnAttestation({
            auditor: _deployer,
            profileDigest: _supplyController.profileDigest(),
            operationId: keccak256(bytes("E2E-OP-1")),
            metadataDigest: keccak256("e2e-metadata-burn-1"),
            amount: amount,
            nonce: _nextNonce(),
            validUntil: uint64(block.timestamp + 1 days),
            vault: address(_vault)
        });
        bytes memory sig = _signBurn(b, _deployerPk);

        vm.startBroadcast(_deployerPk);
        _supplyController.burn(b, sig);
        vm.stopBroadcast();
        console2.log("Burned", amount, "of returned Vault inventory");
    }

    function _nextNonce() internal returns (uint256) {
        return _nonce++;
    }

    function _signMint(ISupplyController.MintAttestation memory a, uint256 signerPk)
        internal
        view
        returns (bytes memory)
    {
        bytes32 structHash = keccak256(
            abi.encode(
                _supplyController.MINT_ATTESTATION_TYPEHASH(),
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
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(signerPk, _domainDigest(structHash));
        return abi.encodePacked(r, s, v);
    }

    function _signBurn(ISupplyController.BurnAttestation memory b, uint256 signerPk)
        internal
        view
        returns (bytes memory)
    {
        bytes32 structHash = keccak256(
            abi.encode(
                _supplyController.BURN_ATTESTATION_TYPEHASH(),
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
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(signerPk, _domainDigest(structHash));
        return abi.encodePacked(r, s, v);
    }

    function _domainDigest(bytes32 structHash) internal view returns (bytes32) {
        bytes32 domainSeparator = keccak256(
            abi.encode(
                keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"),
                keccak256(bytes("RWA-Supply-Attestation")),
                keccak256(bytes("1")),
                block.chainid,
                address(_supplyController)
            )
        );
        return keccak256(abi.encodePacked(bytes2(0x1901), domainSeparator, structHash));
    }
}
