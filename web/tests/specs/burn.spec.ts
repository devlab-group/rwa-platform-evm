import { decodeFunctionData, hashTypedData } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminAssetsPage } from "../pages/AdminAssetsPage";
import { ADDRESSES, CHAIN_ID } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";
import { supplyControllerAbi } from "../../src/lib/abis";

const AUDITOR = `0x${"99".repeat(20)}` as const;
const SIGNATURE = `0x${"ab".repeat(65)}` as const;

test.describe("Assets - burn a Minted record", () => {
  mockOnly();

  test("create a burn, upload the auditor's result and send SupplyController.burn", async ({
    page,
    api,
  }) => {
    api.records.push({
      recordId: "record-001",
      status: "Minted",
      metadataDigest: `0x${"cd".repeat(32)}`,
      cid: "bafymockrecord",
      amount: "1000000000000000000",
      createdAt: new Date().toISOString(),
    });
    const assets = new AdminAssetsPage(page);
    await assets.goto();

    await assets.createBurn("record-001", "0.25");
    await expect(assets.burnRow("record-001").locator(".badge")).toHaveText(
      "Pending",
    );
    const burn = api.burns[0];
    expect(burn.amount).toBe("250000000000000000");

    // Build matching digest fields for the mock signed result
    const typedDataDigest = hashTypedData({
      domain: {
        name: "RWA-Supply-Attestation",
        version: "1",
        chainId: CHAIN_ID,
        verifyingContract: ADDRESSES.supplyController as `0x${string}`,
      },
      types: {
        BurnAttestation: [
          { name: "auditor", type: "address" },
          { name: "profileDigest", type: "bytes32" },
          { name: "operationId", type: "bytes32" },
          { name: "metadataDigest", type: "bytes32" },
          { name: "amount", type: "uint256" },
          { name: "nonce", type: "uint256" },
          { name: "validUntil", type: "uint64" },
          { name: "vault", type: "address" },
        ],
      },
      primaryType: "BurnAttestation",
      message: {
        auditor: AUDITOR,
        profileDigest: api.project.profileDigest as `0x${string}`,
        operationId: burn.operationId as `0x${string}`,
        metadataDigest: burn.metadataDigest as `0x${string}`,
        amount: BigInt(burn.amount!),
        nonce: BigInt(burn.nonce!),
        validUntil: BigInt(burn.validUntil!),
        vault: ADDRESSES.vault as `0x${string}`,
      },
    });
    await assets.uploadBurnSignedResult({
      formatVersion: "1.0",
      auditor: AUDITOR,
      primaryType: "BurnAttestation",
      typedDataDigest,
      signature: SIGNATURE,
      signedAt: "2026-01-01T00:00:00Z",
    });

    // Confirmation precedes broadcast
    await expect(page.getByText("Confirm burn")).toBeVisible();
    expect(await getMockSentTransactions(page)).toHaveLength(0);
    await page.getByRole("button", { name: "Confirm and burn" }).click();
    await expect(page.getByText(/Burn broadcast\. Transaction:/)).toBeVisible();

    const txs = await getMockSentTransactions(page);
    const tx = txs.find(
      (t) => t.to?.toLowerCase() === ADDRESSES.supplyController.toLowerCase(),
    );
    expect(tx, "burn transaction sent to SupplyController").toBeTruthy();
    const { functionName, args } = decodeFunctionData({
      abi: supplyControllerAbi,
      data: tx!.data as `0x${string}`,
    });
    expect(functionName).toBe("burn");
    const attestation = args[0] as Record<string, unknown>;
    expect(attestation.amount).toBe(250000000000000000n);
    expect(attestation.operationId).toBe(burn.operationId);
    expect((args[1] as string).toLowerCase()).toBe(SIGNATURE);
  });

  test("a digest for another amount is rejected before the wallet", async ({
    page,
    api,
  }) => {
    api.records.push({
      recordId: "record-002",
      status: "Minted",
      metadataDigest: `0x${"cd".repeat(32)}`,
      cid: "bafymockrecord",
      amount: "1000000000000000000",
      createdAt: new Date().toISOString(),
    });
    const assets = new AdminAssetsPage(page);
    await assets.goto();
    await assets.createBurn("record-002", "1");
    await expect(assets.burnRow("record-002")).toBeVisible();

    await assets.uploadBurnSignedResult({
      formatVersion: "1.0",
      auditor: AUDITOR,
      primaryType: "BurnAttestation",
      typedDataDigest: `0x${"dd".repeat(32)}`,
      signature: SIGNATURE,
      signedAt: "2026-01-01T00:00:00Z",
    });
    await expect(page.getByText(/different burn/)).toBeVisible();
    expect(await getMockSentTransactions(page)).toHaveLength(0);
  });
});
