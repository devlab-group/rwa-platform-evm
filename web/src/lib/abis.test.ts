import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { toFunctionSelector } from "viem";
import { erc7943Abi } from "./abis";

/** The frozen ERC-7943 ABI surface, shared with the Solidity interface and the server's Go
 * bindings. A selector that drifts here would have the console broadcasting calldata the
 * deployed token does not answer to. */
const vectors = JSON.parse(
  // Relative to web/, which is vitest's root.
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

  // A return type is invisible to a selector, but a fragment that claims no return value
  // makes viem decode nothing where the token actually returns a bool.
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
