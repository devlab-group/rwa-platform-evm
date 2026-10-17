import { describe, expect, it } from "vitest";
import {
  findNetwork,
  networkLabel,
  toAddChainParams,
} from "./networks";

describe("networks", () => {
  it("finds an allow-listed chain by id and returns undefined otherwise", () => {
    expect(findNetwork(31337)?.label).toBe("Local");
    expect(findNetwork(1)?.label).toBe("Ethereum");
    expect(findNetwork(5)).toBeUndefined();
  });

  it.each([
    [1, "Ethereum"],
    [31337, "Local"],
    [999, "Unsupported network"],
    [null, "Unsupported network"],
  ])("labels chain %s as %s", (chainId, label) => {
    expect(networkLabel(chainId)).toBe(label);
  });

  it("builds EIP-3085 params with a 0x-prefixed lowercase hex chain id", () => {
    const local = findNetwork(31337)!;
    expect(toAddChainParams(local)).toEqual({
      chainId: "0x7a69",
      chainName: "Local (Anvil)",
      nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
      rpcUrls: ["http://localhost:8545"],
      blockExplorerUrls: [],
    });
    expect(toAddChainParams(findNetwork(1)!).chainId).toBe("0x1");
  });
});
