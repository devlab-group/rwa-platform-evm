import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { keccak256, toFunctionSelector, toHex } from "viem";
import {
  erc7943Abi,
  incomeDistributorAbi,
  incomeDistributorErrorsAbi,
  oracleStrategyErrorsAbi,
  rwaFactoryAbi,
  rwaFactoryV3Abi,
  supplyControllerAbi,
  vaultAbi,
  vaultErrorsAbi,
} from "./abis";

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

// Ignore compiler-only fields and top-level parameter names; tuple names pin field order
type AbiParam = {
  name: string;
  type: string;
  components?: AbiParam[];
  internalType?: string;
};
function strip(params: readonly AbiParam[], top = true): unknown[] {
  return params.map((p) => ({
    ...(top ? {} : { name: p.name }),
    type: p.type,
    ...(p.components ? { components: strip(p.components, false) } : {}),
  }));
}

// Local runs skip without forge artifacts; CI builds them first and fails if one is missing
const OUT = "../contracts/out";
const FORGE_BUILT = existsSync(`${OUT}/RWAFactory.sol/RWAFactory.json`);

function artifact(contract: string) {
  return (
    JSON.parse(
      readFileSync(`${OUT}/${contract}.sol/${contract}.json`, "utf8"),
    ) as {
      abi: {
        type: string;
        name: string;
        inputs: AbiParam[];
        outputs?: AbiParam[];
      }[];
    }
  ).abi;
}

describe.skipIf(!FORGE_BUILT && !process.env.CI)(
  "pinned ABIs match the forge artifacts",
  () => {
    const cases = [
      ["RWAFactory", rwaFactoryV3Abi],
      ["Vault", vaultAbi],
      ["Vault", vaultErrorsAbi],
      ["OraclePriceStrategy", oracleStrategyErrorsAbi],
      ["IncomeDistributor", incomeDistributorAbi],
      ["IncomeDistributor", incomeDistributorErrorsAbi],
    ] as const;

    it.each(cases)(
      "%s fragments carry the compiled inputs and outputs",
      (contract, abi) => {
        const compiled = artifact(contract);
        for (const item of abi as readonly {
          type: string;
          name: string;
          inputs: readonly AbiParam[];
          outputs?: readonly AbiParam[];
        }[]) {
          const match = compiled.find(
            (c) => c.type === item.type && c.name === item.name,
          );
          expect(
            match,
            `${contract}.${item.name} missing from artifact`,
          ).toBeDefined();
          expect(strip(item.inputs)).toEqual(strip(match!.inputs));
          if (item.type === "function") {
            expect(strip(item.outputs ?? [])).toEqual(
              strip(match!.outputs ?? []),
            );
          }
        }
      },
    );

    it("keeps the rwa-v2 deploy tuple distinct from rwa-v3", () => {
      const v2 = rwaFactoryAbi[0].inputs[0].components.map((c) => c.name);
      const v3 = rwaFactoryV3Abi[0].inputs[0].components.map((c) => c.name);
      expect(v2).not.toContain("minPurchaseQuote");
      expect(v3.slice(0, 8)).toEqual(v2.slice(0, 8));
      expect(v3).toContain("incomeManager");
    });
  },
);
