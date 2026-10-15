import { describe, expect, it } from "vitest";
import { batchChecksum, buildSafeBatch } from "./safeBatch";

const ZERO = "0x0000000000000000000000000000000000000000";

describe("batchChecksum", () => {
  // Independent Transaction Builder checksum vector from @morpho-labs/gnosis-tx-builder 2.0.0 tests
  it("reproduces a reference Transaction Builder checksum", () => {
    const reference = {
      version: "1.0",
      chainId: "1",
      createdAt: 1665179771184,
      meta: {
        name: "Transactions Batch",
        checksum:
          "0x0e8bc7db05f5d3272681a16a52c08de6f1400b8afd76d332e771e5473bcca49e",
        description: "Created from @morpho-labs/gnosis-tx-builder",
        txBuilderVersion: "1.10.0",
        createdFromSafeAddress: ZERO,
        createdFromOwnerAddress: "",
      },
      transactions: [{ to: ZERO, value: "1000000000000000000", data: "0x" }],
    };
    expect(batchChecksum(reference)).toBe(reference.meta.checksum);
  });
});

describe("buildSafeBatch", () => {
  it("builds raw-calldata transactions whose checksum survives the importer's check", () => {
    const safe = "0x9a7F3dfDEeBb51eAbBbA3c8eCCf8cF2a983A1C72";
    const batch = buildSafeBatch({
      chainId: 31337,
      safe,
      name: "Pause trading",
      description: "test",
      calls: [
        {
          to: "0x00000000000000000000000000000000000000aa",
          data: "0x8456cb59",
        },
      ],
      createdAt: 1,
    });
    expect(batch.chainId).toBe("31337");
    expect(batch.meta.createdFromSafeAddress).toBe(safe);
    // Encode each value as a string
    expect(batch.transactions).toEqual([
      {
        to: "0x00000000000000000000000000000000000000aa",
        value: "0",
        data: "0x8456cb59",
      },
    ]);
    // Changing the name does not change the checksum
    const { checksum, ...rest } = batch.meta;
    expect(
      batchChecksum({ ...batch, meta: { ...rest, name: "Renamed" } }),
    ).toBe(checksum);
  });
});
