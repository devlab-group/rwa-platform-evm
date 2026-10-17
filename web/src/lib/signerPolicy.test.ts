import { describe, expect, it } from "vitest";
import type { components } from "./api-types";
import { buildSignerPolicy } from "./signerPolicy";

type Project = components["schemas"]["Project"];

const PROJECT: Project = {
  chainId: 31337,
  auditor: "0x00000000000000000000000000000000000000a1",
  profileDigest: `0x${"11".repeat(32)}`,
  addresses: {
    supplyController: "0x0000000000000000000000000000000000000044",
    vault: "0x0000000000000000000000000000000000000055",
  },
};
const UUID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301";

describe("buildSignerPolicy", () => {
  it("maps the project fields to the signer policy keys, with chainId as a decimal string", () => {
    expect(buildSignerPolicy(PROJECT, UUID)).toEqual({
      chainId: "31337",
      controller: "0x0000000000000000000000000000000000000044",
      vault: "0x0000000000000000000000000000000000000055",
      auditor: "0x00000000000000000000000000000000000000a1",
      projectId: UUID,
      profileDigest: `0x${"11".repeat(32)}`,
    });
  });

  it("accepts chain id 0 as present", () => {
    expect(buildSignerPolicy({ ...PROJECT, chainId: 0 }, UUID)?.chainId).toBe(
      "0",
    );
  });

  it.each([
    ["no project", undefined, UUID],
    ["no project UUID", PROJECT, undefined],
    ["an empty project UUID", PROJECT, ""],
    ["no chainId", { ...PROJECT, chainId: undefined }, UUID],
    ["no auditor", { ...PROJECT, auditor: undefined }, UUID],
    ["no profileDigest", { ...PROJECT, profileDigest: undefined }, UUID],
    [
      "no supplyController",
      { ...PROJECT, addresses: { vault: PROJECT.addresses!.vault } },
      UUID,
    ],
    [
      "no vault",
      {
        ...PROJECT,
        addresses: { supplyController: PROJECT.addresses!.supplyController },
      },
      UUID,
    ],
    ["no addresses", { ...PROJECT, addresses: undefined }, UUID],
  ])("returns null with %s", (_name, project, uuid) => {
    expect(buildSignerPolicy(project as Project | undefined, uuid)).toBeNull();
  });
});
