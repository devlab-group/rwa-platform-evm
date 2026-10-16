// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";

/// @notice Verifies OpenZeppelin StandardMerkleTree roots and proofs from
///         shared/vectors/income-merkle.json, also reproduced by the server
contract IncomeMerkleVectorsTest is TestBase {
    function setUp() public override {
        super.setUp();
        vm.roll(100);
        quoteToken.mint(incomeManager, 100_000_000);
        vm.prank(incomeManager);
        quoteToken.approve(address(income), type(uint256).max);
    }

    function test_claim_acceptsStandardMerkleTreeVectors() public {
        string memory json = vm.readFile("../shared/vectors/income-merkle.json");
        uint256 cases;
        for (; vm.keyExistsJson(json, string.concat(".cases[", vm.toString(cases), "]")); ++cases) {
            _claimVectorCase(json, string.concat(".cases[", vm.toString(cases), "]"));
        }
        assertEq(cases, 2, "vector cases");
    }

    function _claimVectorCase(string memory json, string memory c) internal {
        uint256 n;
        while (vm.keyExistsJson(json, string.concat(c, ".entries[", vm.toString(n), "]"))) {
            ++n;
        }
        uint256 total;
        for (uint256 i; i < n; ++i) {
            total += vm.parseUint(vm.parseJsonString(json, string.concat(c, ".entries[", vm.toString(i), "].amount")));
        }
        vm.prank(incomeManager);
        uint256 id = income.createDistribution(
            vm.parseJsonBytes32(json, string.concat(c, ".root")),
            total,
            50,
            uint64(block.timestamp + 7 days),
            "ipfs://vector",
            keccak256("vector")
        );
        for (uint256 i; i < n; ++i) {
            _claimVectorEntry(json, string.concat(c, ".entries[", vm.toString(i), "]"), id);
        }
        assertEq(income.getDistribution(id).claimed, total);
    }

    function _claimVectorEntry(string memory json, string memory e, uint256 id) internal {
        address who = vm.parseJsonAddress(json, string.concat(e, ".account"));
        uint256 amount = vm.parseUint(vm.parseJsonString(json, string.concat(e, ".amount")));
        assertEq(
            keccak256(bytes.concat(keccak256(abi.encode(who, amount)))),
            vm.parseJsonBytes32(json, string.concat(e, ".leaf"))
        );
        bytes32[] memory proof = vm.parseJsonBytes32Array(json, string.concat(e, ".proof"));

        vm.prank(complianceOperator);
        compliance.setStatus(who, IComplianceRegistry.ComplianceStatus.Allowed, 0);
        uint256 before = quoteToken.balanceOf(who);
        vm.prank(who);
        income.claim(id, amount, proof);
        assertEq(quoteToken.balanceOf(who) - before, amount);
    }
}
