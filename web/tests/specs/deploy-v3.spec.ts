import { decodeFunctionData, keccak256, toBytes } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminSetupPage } from "../pages/AdminSetupPage";
import { FACTORY_ADDRESS } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";
import { rwaFactoryV3Abi } from "../../src/lib/abis";

const ADMIN = `0x${"aa".repeat(20)}`;
const AUDITOR = "0x9999999999999999999999999999999999999999";
const QUOTE_TOKEN = "0x8888888888888888888888888888888888888888";
const INCOME_MANAGER = `0x${"bb".repeat(20)}`;
const FEED = `0x${"fe".repeat(20)}`;

test.describe("Setup - rwa-v3 factory", () => {
  mockOnly();

  async function reachDeployForm(page: import("@playwright/test").Page) {
    const setup = new AdminSetupPage(page);
    await setup.goto();
    await setup.createProfileFor({ tokenUnit: "gram", tokenDecimals: 18 });
    await expect(setup.createdProfileStatus()).toContainText("Persisted");
    return setup;
  }

  async function fillAddresses(page: import("@playwright/test").Page) {
    await page.locator("#name").fill("Gold Bar Token");
    await page.locator("#symbol").fill("GOLD");
    await page.locator("#quoteToken").fill(QUOTE_TOKEN);
    for (const id of [
      "admin",
      "complianceOperator",
      "treasurer",
      "redemptionManager",
      "treasury",
    ]) {
      await page.locator(`#${id}`).fill(ADMIN);
    }
    await page.locator("#auditor").fill(AUDITOR);
    await page.locator("#incomeManager").fill(INCOME_MANAGER);
  }

  test("an unreadable factory version blocks deployment with a clear message", async ({
    page,
    api,
  }) => {
    api.factoryVersion = "";
    await reachDeployForm(page);
    await expect(page.getByText(/deployment is blocked/i)).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Review deployment" }),
    ).toHaveCount(0);
  });

  test("an oracle deployment broadcasts the rwa-v3 config", async ({
    page,
    api,
  }) => {
    api.factoryVersion = "rwa-v3";
    const setup = await reachDeployForm(page);
    const projectId = await setup.generatedProjectId();

    await fillAddresses(page);
    await page.locator("#strategyKind").selectOption("Oracle");
    // Fixed-only inputs are gone for an oracle
    await expect(page.locator("#pricer")).toHaveCount(0);
    await expect(page.locator("#purchasePrice")).toHaveCount(0);
    await page.locator("#priceFeed").fill(FEED);
    await page.locator("#priceMaxAge").fill("3600");
    await page.locator("#purchaseSpreadBps").fill("50");
    await page.locator("#redemptionSpreadBps").fill("75");
    await page.locator("#minPurchase").fill("25");

    await setup.reviewDeployment();
    await expect(page.getByText("Confirm deployment")).toBeVisible();
    await setup.confirmDeploy();
    await expect(setup.deployedConfirmation()).toBeVisible();

    const txs = await getMockSentTransactions(page);
    const deployTx = txs.find(
      (t) => t.to?.toLowerCase() === FACTORY_ADDRESS.toLowerCase(),
    );
    expect(deployTx, "a deploy tx was broadcast").toBeTruthy();
    const { functionName, args } = decodeFunctionData({
      abi: rwaFactoryV3Abi,
      data: deployTx!.data as `0x${string}`,
    });
    expect(functionName).toBe("deploy");
    const config = args[0] as Record<string, unknown>;
    expect(config.projectId).toBe(keccak256(toBytes(projectId)));
    expect(config).toMatchObject({
      strategyKind: 1,
      priceMaxAge: 3600n,
      purchaseSpreadBps: 50,
      redemptionSpreadBps: 75,
      // The mock wallet reports 18 decimals for the quote token
      minPurchaseQuote: 25_000_000_000_000_000_000n,
      purchasePricePerWholeToken: 0n,
      redemptionPricePerWholeToken: 0n,
    });
    expect(BigInt(config.pricer as string)).toBe(0n);
    expect((config.priceFeed as string).toLowerCase()).toBe(FEED);
    expect((config.incomeManager as string).toLowerCase()).toBe(INCOME_MANAGER);
  });

  test("a fixed deployment keeps the price inputs and sends no oracle parameters", async ({
    page,
    api,
  }) => {
    api.factoryVersion = "rwa-v3";
    const setup = await reachDeployForm(page);
    await fillAddresses(page);
    await page.locator("#pricer").fill(ADMIN);
    await page.locator("#purchasePrice").fill("1.5");
    await page.locator("#redemptionPrice").fill("1");
    await setup.reviewDeployment();
    await setup.confirmDeploy();
    await expect(setup.deployedConfirmation()).toBeVisible();

    const txs = await getMockSentTransactions(page);
    const deployTx = txs.find(
      (t) => t.to?.toLowerCase() === FACTORY_ADDRESS.toLowerCase(),
    );
    const config = decodeFunctionData({
      abi: rwaFactoryV3Abi,
      data: deployTx!.data as `0x${string}`,
    }).args[0] as Record<string, unknown>;
    expect(config).toMatchObject({
      strategyKind: 0,
      purchasePricePerWholeToken: 1_500_000_000_000_000_000n,
      redemptionPricePerWholeToken: 1_000_000_000_000_000_000n,
      minPurchaseQuote: 0n,
      priceMaxAge: 0n,
    });
    expect(BigInt(config.priceFeed as string)).toBe(0n);
  });
});
