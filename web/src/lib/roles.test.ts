import { describe, expect, it } from "vitest";
import type { components } from "./api-types";
import {
  adminTransferTargets,
  DEFAULT_ADMIN_ROLE_HASH,
  hasRole,
  roleHash,
  roleTargets,
  ROLES,
} from "./roles";

type Addresses = components["schemas"]["Addresses"];

const ADDRESSES: Addresses = {
  token: "0x0000000000000000000000000000000000000010",
  compliance: "0x0000000000000000000000000000000000000020",
  supplyController: "0x0000000000000000000000000000000000000030",
  vault: "0x0000000000000000000000000000000000000040",
  redemptionEscrow: "0x0000000000000000000000000000000000000050",
  strategy: "0x0000000000000000000000000000000000000060",
  quoteToken: "0x0000000000000000000000000000000000000070",
};

const roles = {
  DEFAULT_ADMIN_ROLE: ["0xAbc0000000000000000000000000000000000001"],
  TREASURER_ROLE: [
    "0x1111111111111111111111111111111111111111",
    "0x2222222222222222222222222222222222222222",
  ],
};

describe("hasRole", () => {
  it("matches a holder case-insensitively", () => {
    expect(
      hasRole(roles, ROLES.treasurer, "0x1111111111111111111111111111111111111111"),
    ).toBe(true);
    // Different case, same address.
    expect(
      hasRole(roles, ROLES.admin, "0xabc0000000000000000000000000000000000001"),
    ).toBe(true);
  });

  it("returns false for a non-holder", () => {
    expect(
      hasRole(roles, ROLES.treasurer, "0x9999999999999999999999999999999999999999"),
    ).toBe(false);
  });

  it("returns false for an unknown role or a role with no holders", () => {
    expect(hasRole(roles, ROLES.pauser, "0x1111111111111111111111111111111111111111")).toBe(
      false,
    );
  });

  it("returns false when roles or address are missing", () => {
    expect(hasRole(undefined, ROLES.admin, "0xabc")).toBe(false);
    expect(hasRole(roles, ROLES.admin, null)).toBe(false);
    expect(hasRole(roles, ROLES.admin, undefined)).toBe(false);
  });
});

describe("roleHash", () => {
  it("computes the on-chain bytes32 role ids (OZ keccak256 of the name)", () => {
    expect(roleHash(ROLES.pauser)).toBe(
      "0x65d7a28e3265b37a6474929f336521b332c1681b933f6cb9f3376673440d862a",
    );
    expect(roleHash(ROLES.pricer)).toBe(
      "0xc6823861ee2bb2198ce6b1fd6faf4c8f44f745bc804aca4a762f67e0d507fd8a",
    );
    expect(roleHash(ROLES.treasurer)).toBe(
      "0x3496e2e73c4d42b75d702e60d9e48102720b8691234415963a5a857b86425d07",
    );
    expect(roleHash(ROLES.redemptionManager)).toBe(
      "0xe5bea7d829f723a95a0c83a655765be37702fa584514c1e2a20867d04b58478e",
    );
    expect(roleHash(ROLES.compliance)).toBe(
      "0x442a94f1a1fac79af32856af2a64f63648cfa2ef3b98610a5bb7cbec4cee6985",
    );
  });

  it("uses the zero hash for DEFAULT_ADMIN_ROLE (not keccak256 of the name)", () => {
    expect(roleHash(ROLES.admin)).toBe(DEFAULT_ADMIN_ROLE_HASH);
    expect(DEFAULT_ADMIN_ROLE_HASH).toBe(`0x${"0".repeat(64)}`);
  });
});

describe("roleTargets / adminTransferTargets", () => {
  it("maps each granular role to the contracts it is held on", () => {
    expect(roleTargets(ROLES.pauser, ADDRESSES)).toEqual([ADDRESSES.token]);
    expect(roleTargets(ROLES.pricer, ADDRESSES)).toEqual([ADDRESSES.strategy]);
    expect(roleTargets(ROLES.treasurer, ADDRESSES)).toEqual([
      ADDRESSES.vault,
      ADDRESSES.redemptionEscrow,
    ]);
    expect(roleTargets(ROLES.redemptionManager, ADDRESSES)).toEqual([
      ADDRESSES.redemptionEscrow,
    ]);
    expect(roleTargets(ROLES.compliance, ADDRESSES)).toEqual([
      ADDRESSES.compliance,
    ]);
  });

  it("returns no targets when addresses are missing", () => {
    expect(roleTargets(ROLES.pricer, undefined)).toEqual([]);
  });

  it("targets every governance contract for an admin transfer (not the quote token)", () => {
    expect(adminTransferTargets(ADDRESSES)).toEqual([
      ADDRESSES.token,
      ADDRESSES.compliance,
      ADDRESSES.supplyController,
      ADDRESSES.vault,
      ADDRESSES.redemptionEscrow,
      ADDRESSES.strategy,
    ]);
  });
});
