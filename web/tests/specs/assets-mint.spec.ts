import { decodeFunctionData } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminAssetsPage } from "../pages/AdminAssetsPage";
import { ADDRESSES } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";
import { supplyControllerAbi } from "../../src/lib/abis";

// Matches the default mock project's auditor (mock-api ADDRESSES/addr("99")).
const AUDITOR = `0x${"99".repeat(20)}`;
// A 65-byte signature; it is encoded as `bytes` calldata, so it must be real hex
const SIGNATURE = `0x${"ab".repeat(65)}`;

test.describe("Assets - create record and mint via auditor signature", () => {
  mockOnly();

  test("uploading the auditor signature broadcasts SupplyController.mint from the wallet", async ({ page }) => {
    const assets = new AdminAssetsPage(page);
    await assets.goto();

    // 1 whole token is 1e18 minimal units at 18 decimals; the record stores
    // minimal units, and the MintAttestation carries them
    await assets.createRecord({ recordId: "record-001", amount: "1" });
    await expect(assets.recordRow("record-001")).toBeVisible();
    // The record stays Pending until the server observes the on-chain Minted
    // event, which the mock does not simulate
    await expect(assets.recordRow("record-001").locator(".badge")).toHaveText("Pending");

    await assets.uploadSignature({
      recordId: "record-001",
      auditor: AUDITOR,
      primaryType: "MintAttestation",
      typedDataDigest: `0x${"dd".repeat(32)}`,
      signature: SIGNATURE,
    });

    await expect(page.getByText(/Mint broadcast\. Transaction:/)).toBeVisible();

    // The connected wallet sent the mint to the SupplyController with the
    // record's attestation fields and the auditor's signature
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
    // The browser rejects a JSON object that lacks the signature fields
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
    // Seed a stored profile whose assetSchema declares two fields; the
    // Create-record metadata editor should open with a skeleton built from that
    // schema instead of an empty object
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
    // The skeleton has both properties, with a default for each type
    await expect(metadata).toHaveValue(/"serial":\s*""/);
    await expect(metadata).toHaveValue(/"weightGrams":\s*0/);
    // The Record ID hint uses the profile's recordIdLabel
    await expect(page.getByText(/the serial number/i)).toBeVisible();
  });

  test("package download is available and fetches authenticated", async ({ page }) => {
    // The `api` fixture seeds the admin session (src/lib/authSession.ts), and
    // the download must send it as a bearer token, which a plain <a href> cannot
    const assets = new AdminAssetsPage(page);
    await assets.goto();

    await assets.createRecord({ recordId: "record-002", amount: "500000000000000000" });
    const downloadButton = assets.recordRow("record-002").getByRole("button", { name: "Download .rwa" });
    await expect(downloadButton).toBeVisible();

    // A plain <a href> cannot send an Authorization header, so the download
    // uses an authenticated fetch; the package endpoint is admin-only
    const [request, download] = await Promise.all([
      page.waitForRequest((r) => r.url().includes("/package") && r.method() === "GET"),
      page.waitForEvent("download"),
      downloadButton.click(),
    ]);
    expect(request.headers()["authorization"]).toMatch(/^Bearer /);
    expect(download.suggestedFilename()).toBe("record-002.rwa");
  });

  test("Signer policy block downloads a valid policy.json for the auditor", async ({ page, api }) => {
    // Seed a stored profile whose raw document carries the project UUID, which
    // the signer policy pins. The default mock project is already deployed, so
    // its supplyController, vault, auditor, profileDigest and chainId are set.
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
    // The six required policy keys, and nothing else
    expect(Object.keys(policy).sort()).toEqual([
      "auditor",
      "chainId",
      "controller",
      "profileDigest",
      "projectId",
      "vault",
    ]);
    // chainId is a decimal string and projectId is the UUID, not a hash
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
