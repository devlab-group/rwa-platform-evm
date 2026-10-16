import { decodeFunctionData } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { ADDRESSES } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";
import { vaultAbi } from "../../src/lib/abis";
import { sectionByHeading } from "../fixtures/helpers";

test.describe("Security - rwa-v3", () => {
  mockOnly();

  test.beforeEach(({ api }) => {
    api.project.version = "rwa-v3";
    api.project.quoteDecimals = 6;
    api.project.strategyKind = "Fixed";
    api.project.minPurchaseQuote = "25000000";
    api.project.addresses = {
      ...ADDRESSES,
      incomeDistributor: `0x${"9a".repeat(20)}`,
    };
  });

  test("the admin wallet sets the minimum purchase on the Vault", async ({
    page,
  }) => {
    await page.goto("/admin/security");
    await page.getByRole("button", { name: "Connect wallet" }).click();
    await page.getByRole("button", { name: "Disconnect" }).waitFor();

    const section = sectionByHeading(page, "Minimum purchase");
    await expect(section.getByText("25", { exact: true })).toBeVisible();
    await section.getByLabel(/Minimum purchase \(whole quote-token/).fill("40");
    await section.getByRole("button", { name: "Set minimum purchase" }).click();

    await expect(section.getByText("Minimum purchase updated.")).toBeVisible();
    const [tx] = await getMockSentTransactions(page);
    expect(tx.to?.toLowerCase()).toBe(ADDRESSES.vault.toLowerCase());
    expect(
      decodeFunctionData({ abi: vaultAbi, data: tx.data as `0x${string}` }),
    ).toMatchObject({
      functionName: "setMinPurchaseQuote",
      args: [40_000_000n],
    });
  });

  test("an oracle strategy shows its parameters and hides the price setters", async ({
    page,
    api,
  }) => {
    api.project.strategyKind = "Oracle";
    api.project.oracle = {
      feed: `0x${"fe".repeat(20)}`,
      maxAge: 3600,
      purchaseSpreadBps: 50,
      redemptionSpreadBps: 75,
      feedDecimals: 8,
      available: false,
      error: "StalePrice",
      updatedAt: Math.floor(Date.now() / 1000) - 7200,
    };
    await page.goto("/admin/security");

    const oracle = sectionByHeading(page, "Price oracle");
    await expect(oracle.getByText("StalePrice")).toBeVisible();
    await expect(oracle.getByText("3600 s")).toBeVisible();
    await expect(oracle.getByText("50 bps")).toBeVisible();
    await expect(page.getByRole("heading", { name: "Update prices" })).toHaveCount(0);
  });

  test("a fixed strategy keeps the price setters", async ({ page }) => {
    await page.goto("/admin/security");
    await expect(page.getByRole("heading", { name: "Update prices" })).toBeVisible();
  });
});
