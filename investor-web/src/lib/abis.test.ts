import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import {
  incomeDistributorAbi,
  oracleErrorsAbi,
  redemptionEscrowAbi,
  tokenErrorsAbi,
  vaultAbi,
  vaultErrorsAbi,
} from "./abis";

// Ignore compiler-only fields and top-level parameter names; tuple names pin field order
type AbiParam = {
  name: string;
  type: string;
  components?: AbiParam[];
  internalType?: string;
};
type AbiItem = {
  type: string;
  name?: string;
  inputs?: readonly AbiParam[];
  outputs?: readonly AbiParam[];
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
const FORGE_BUILT = existsSync(`${OUT}/Vault.sol/Vault.json`);

function artifact(contract: string): AbiItem[] {
  return (
    JSON.parse(
      readFileSync(`${OUT}/${contract}.sol/${contract}.json`, "utf8"),
    ) as {
      abi: AbiItem[];
    }
  ).abi;
}

describe.skipIf(!FORGE_BUILT && !process.env.CI)(
  "pinned ABIs match the forge artifacts",
  () => {
    const cases = [
      ["Vault", vaultAbi],
      ["Vault", vaultErrorsAbi],
      ["RedemptionEscrow", redemptionEscrowAbi],
      ["RWAToken", tokenErrorsAbi],
      ["OraclePriceStrategy", oracleErrorsAbi],
      ["IncomeDistributor", incomeDistributorAbi],
    ] as const;

    it.each(cases)(
      "%s fragments carry the compiled inputs and outputs",
      (contract, abi) => {
        const compiled = artifact(contract);
        for (const item of abi as readonly AbiItem[]) {
          const match = compiled.find(
            (c) => c.type === item.type && c.name === item.name,
          );
          expect(
            match,
            `${contract}.${item.name} missing from artifact`,
          ).toBeDefined();
          expect(strip(item.inputs ?? [])).toEqual(strip(match!.inputs ?? []));
          if (item.type === "function") {
            expect(strip(item.outputs ?? [])).toEqual(
              strip(match!.outputs ?? []),
            );
          }
        }
      },
    );
  },
);
