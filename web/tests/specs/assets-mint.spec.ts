import { decodeFunctionData } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminAssetsPage } from "../pages/AdminAssetsPage";
import { ADDRESSES } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";
import { supplyControllerAbi } from "../../src/lib/abis";

// Matches the default mock project's auditor (mock-api ADDRESSES/addr("99")).
const AUDITOR = `0x${"99".repeat(20)}`;
// A valid-hex 65-byte signature — now encoded as on-chain `bytes` calldata, so
// it must be real hex (the old placeholder text would fail encodeFunctionData).
const SIGNATURE = `0x${"ab".repeat(65)}`;

test.describe("Assets — create record and mint via auditor signature", () => {
  mockOnly();

  test("uploading the auditor signature broadcasts SupplyController.mint from the wallet", async ({ page }) => {
    const assets = new AdminAssetsPage(page);
    await assets.goto();

    // "1" whole token → 1e18 minimal units (project decimals = 18); the record
    // is stored in minimal units, which is what the MintAttestation carries.
    await assets.createRecord({ recordId: "record-001", amount: "1" });
    await expect(assets.recordRow("record-001")).toBeVisible();
    // The record starts Pending; it flips to Minted only once the server
    // observes the on-chain Minted event (not simulated by the mock).
    await expect(assets.recordRow("record-001").locator(".badge")).toHaveText("Pending");

    await assets.uploadSignature({
      recordId: "record-001",
      auditor: AUDITOR,
      primaryType: "MintAttestation",
      typedDataDigest: `0x${"dd".repeat(32)}`,
      signature: SIGNATURE,
    });

    await expect(page.getByText(/Mint broadcast\. Transaction:/)).toBeVisible();

    // The mint was broadcast to the SupplyController from the connected wallet,
    // carrying the record's attestation fields and the auditor's signature.
    const txs = await getMockSentTransactions(page);
    const mintTx = txs.find(
      (t) => t.to?.toLowerCase() === ADDRESSES.supplyController.toLowerCase(),
    );
    expect(mintTx, "a mint tx to the SupplyController was broadcast").toBeTruthy();

    const { functionName, args } = decodeFunctionData({
      abi: supplyControllerAbi,
      data: mintTx!.data as `0x${string}`,
    });
    expect(functionName).toBe("mint");
    const attestation = args[0] as Record<string, unknown>;
    expect(attestation.amount).toBe(1000000000000000000n);
    expect((attestation.auditor as string).toLowerCase()).toBe(AUDITOR.toLowerCase());
    expect((attestation.vault as string).toLowerCase()).toBe(ADDRESSES.vault.toLowerCase());
    expect((args[1] as string).toLowerCase()).toBe(SIGNATURE.toLowerCase());
  });

  test("rejects a file that is not a signed-result.json before relaying", async ({ page }) => {
    const assets = new AdminAssetsPage(page);
    await assets.goto();
    await assets.createRecord({ recordId: "record-003", amount: "1000000000000000000" });

    await page.locator("#sigRecordId").fill("record-003");
    // A JSON object missing the signature fields must be rejected client-side.
    await assets.uploadSignedResultFile(
      "wrong.json",
      JSON.stringify({ hello: "world" }),
    );
    await expect(page.getByText(/missing field\(s\)/i)).toBeVisible();
    // The mint button stays disabled with no valid parsed result.
    await expect(
      page.getByRole("button", { name: "Upload signature & mint" }),
    ).toBeDisabled();
  });

  test("prefills the Metadata editor from the profile's assetSchema", async ({ page, api }) => {
    // Seed a persisted profile whose assetSchema declares two fields; the
    // Create-record Metadata editor should open pre-filled with a skeleton
    // built from that schema instead of an empty object.
    api.createdProfiles.set("proj-schema", {
      profileDigest: "0xdig",
      tokenDecimals: 18,
      tokenUnit: "gram",
      profile: {
        profileVersion: "1.0",
        projectId: "proj-schema",
        assetType: "gold",
        tokenUnit: "gram",
        tokenDecimals: 18,
        recordIdLabel: "Serial number",
        assetSchema: {
          type: "object",
          properties: {
            serial: { type: "string" },
            weightGrams: { type: "number" },
          },
          required: ["serial"],
        },
      },
    });

    const assets = new AdminAssetsPage(page);
    await assets.goto();

    const metadata = page.locator("#assetJson");
    // Skeleton derived from the schema: both properties present, typed defaults.
    await expect(metadata).toHaveValue(/"serial":\s*""/);
    await expect(metadata).toHaveValue(/"weightGrams":\s*0/);
    // The Record ID hint is made concrete from the profile's recordIdLabel.
    await expect(page.getByText(/the serial number/i)).toBeVisible();
  });

  test("package download is available and fetches authenticated", async ({ page }) => {
    // The operator's bearer session (see src/lib/authSession.ts) is already
    // seeded in memory by the `api` fixture — the download has to attach it,
    // which a plain <a href> can't do.
    const assets = new AdminAssetsPage(page);
    await assets.goto();

    await assets.createRecord({ recordId: "record-002", amount: "500000000000000000" });
    const downloadButton = assets.recordRow("record-002").getByRole("button", { name: "Download .rwa" });
    await expect(downloadButton).toBeVisible();

    // A plain <a href> can't attach an Authorization header, so the download
    // goes through an authenticated fetch instead — the package endpoint is
    // operator-only and would 403 without it.
    const [request, download] = await Promise.all([
      page.waitForRequest((r) => r.url().includes("/package") && r.method() === "GET"),
      page.waitForEvent("download"),
      downloadButton.click(),
    ]);
    expect(request.headers()["authorization"]).toMatch(/^Bearer /);
    expect(download.suggestedFilename()).toBe("record-002.rwa");
  });

  test("Signer policy block downloads a valid policy.json for the auditor", async ({ page, api }) => {
    // Seed a persisted profile whose raw doc carries the project UUID — the
    // signer policy pins that UUID (not a hash). The default project is already
    // deployed (supplyController/vault/auditor/profileDigest/chainId present).
    const uuid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee";
    api.createdProfiles.set(uuid, {
      profileDigest: `0x${"ce".repeat(32)}`,
      tokenDecimals: 18,
      tokenUnit: "gram",
      profile: {
        profileVersion: "1.0",
        projectId: uuid,
        assetType: "gold",
        tokenUnit: "gram",
        tokenDecimals: 18,
      },
    });

    const assets = new AdminAssetsPage(page);
    await assets.goto();

    await expect(
      page.getByRole("heading", { name: /Signer policy/ }),
    ).toBeVisible();
    await expect(page.getByText(uuid)).toBeVisible();

    const [download] = await Promise.all([
      page.waitForEvent("download"),
      page.getByRole("button", { name: "Download policy.json" }).click(),
    ]);
    expect(download.suggestedFilename()).toBe("policy.json");

    const stream = await download.createReadStream();
    const content = await new Promise<string>((resolve, reject) => {
      const chunks: Buffer[] = [];
      stream.on("data", (c: Buffer) => chunks.push(Buffer.from(c)));
      stream.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
      stream.on("error", reject);
    });

    const policy = JSON.parse(content) as Record<string, unknown>;
    // Exactly the six keys the signer's DisallowUnknownFields loader accepts.
    expect(Object.keys(policy).sort()).toEqual([
      "auditor",
      "chainId",
      "controller",
      "profileDigest",
      "projectId",
      "vault",
    ]);
    // chainId is a decimal string, projectId is the UUID (not a hash).
    expect(typeof policy.chainId).toBe("string");
    expect(policy.chainId).toBe("31337");
    expect(policy.projectId).toBe(uuid);
    expect((policy.controller as string).toLowerCase()).toBe(
      ADDRESSES.supplyController.toLowerCase(),
    );
    expect((policy.vault as string).toLowerCase()).toBe(
      ADDRESSES.vault.toLowerCase(),
    );
  });
});
