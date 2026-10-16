import { decodeFunctionData } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { ADDRESSES } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";
import { incomeDistributorAbi } from "../../src/lib/abis";
import { InvestorPage } from "../pages/InvestorPage";

const DISTRIBUTOR = `0x${"99".repeat(20)}`;
const PROOF = [`0x${"ab".repeat(32)}`];

test.describe("Investor - rwa-v3 surfaces", () => {
  mockOnly();

  test("refuses a purchase below the project minimum before the wallet opens", async ({ page, api }) => {
    // The mock Vault quotes 1 token at 1000000, under this minimum
    api.project.minPurchaseQuote = "2000000";
    api.project.quoteDecimals = 6;
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await expect(investor.buySection).toContainText("Minimum purchase: 2");
    await investor.previewBuy("1");
    await expect(investor.buySection.getByRole("alert")).toContainText("below the project minimum");
    await expect(
      investor.buySection.getByRole("button", { name: "1. Approve quote token spend" }),
    ).toHaveCount(0);
    expect(await getMockSentTransactions(page)).toHaveLength(0);
  });

  test("an unavailable oracle disables new buys and redemption requests", async ({ page, api }) => {
    api.project.strategyKind = "Oracle";
    api.project.oracle = {
      feed: `0x${"12".repeat(20)}`,
      maxAge: 3600,
      purchaseSpreadBps: 0,
      redemptionSpreadBps: 0,
      feedDecimals: 8,
      available: false,
      error: "StalePrice",
      updatedAt: 1700000000,
    };
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await expect(page.getByText(/Prices unavailable: the price feed has not updated/)).toBeVisible();
    await investor.fillBuyAmount("1");
    await expect(investor.buyPreviewButton).toBeDisabled();
    await page.locator("#redeem-amount").fill("1");
    await expect(investor.redemptionSection.getByRole("button", { name: "Preview quote" })).toBeDisabled();
  });

  test("claims an income entitlement with the server proof", async ({ page, api }) => {
    api.project.addresses = { ...ADDRESSES, incomeDistributor: DISTRIBUTOR };
    api.project.quoteDecimals = 6;
    api.incomeClaims.push({
      distributionId: "4",
      account: "0x1111111111111111111111111111111111111111",
      amount: "1500000",
      proof: PROOF,
      merkleRoot: `0x${"cd".repeat(32)}`,
      claimDeadline: 4102444800,
      snapshotURI: "ipfs://bafy",
      status: "Claimable",
    });
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    const income = page.getByRole("region", { name: "Income" });
    await expect(income).toContainText("Distribution 4");
    await income.getByRole("button", { name: "Claim" }).click();
    await income.getByRole("button", { name: "Confirm claim" }).click();
    await expect(income.getByText(/Claim submitted:/)).toBeVisible();

    const sent = (await getMockSentTransactions(page)).at(-1);
    expect(sent?.to?.toLowerCase()).toBe(DISTRIBUTOR);
    const decoded = decodeFunctionData({ abi: incomeDistributorAbi, data: sent!.data as `0x${string}` });
    expect(decoded.functionName).toBe("claim");
    expect(decoded.args).toEqual([4n, 1500000n, PROOF]);
  });
});
