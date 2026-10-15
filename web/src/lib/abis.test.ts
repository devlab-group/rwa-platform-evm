import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { keccak256, toFunctionSelector, toHex } from "viem";
import { erc7943Abi, supplyControllerAbi } from "./abis";

/*
 * Checks ERC-7943 ABI fragments against shared vectors used by Solidity and Go;
 * selector drift would produce calls the deployed token cannot handle
 */
const vectors = JSON.parse(
  // Relative to Vitest's web/ root
  readFileSync("../shared/vectors/erc7943-abi.json", "utf8"),
) as {
  functions: Record<
    string,
    { signature: string; selector: string; outputs?: string[] }
  >;
};

describe("erc7943Abi", () => {
  it.each(["setFrozenTokens", "forcedTransfer"])(
    "encodes %s to the pinned selector",
    (name) => {
      const fragment = erc7943Abi.find(
        (entry) => entry.type === "function" && entry.name === name,
      );
      expect(fragment, `${name} missing from erc7943Abi`).toBeDefined();
      expect(toFunctionSelector(fragment!)).toBe(
        vectors.functions[name].selector,
      );
    },
  );

  // Return types do not affect selectors; pin bool outputs so viem decodes the token response
  it.each(["setFrozenTokens", "forcedTransfer"])(
    "declares %s's pinned return type",
    (name) => {
      const fragment = erc7943Abi.find(
        (entry) => entry.type === "function" && entry.name === name,
      ) as {
        outputs: readonly { type: string }[];
      };
      expect(fragment.outputs.map((output) => output.type)).toEqual(
        vectors.functions[name].outputs,
      );
    },
  );
});

const typehashes = JSON.parse(
  readFileSync("../shared/vectors/typehashes.json", "utf8"),
) as Record<string, string>;

// Field names and order determine the typehash
describe("supplyControllerAbi attestation tuples", () => {
  it.each([
    ["mint", "MintAttestation", "mintTypehash"],
    ["burn", "BurnAttestation", "burnTypehash"],
  ] as const)("%s tuple hashes to the pinned typehash", (name, type, key) => {
    const fragment = supplyControllerAbi.find((e) => e.name === name)!;
    const fields = fragment.inputs[0].components
      .map((c) => `${c.type} ${c.name}`)
      .join(",");
    expect(keccak256(toHex(`${type}(${fields})`))).toBe(typehashes[key]);
  });
});
