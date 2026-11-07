import { decodeFunctionData } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminSetupPage } from "../pages/AdminSetupPage";
import { FACTORY_ADDRESS } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";
import { rwaFactoryV4Abi } from "../../src/lib/abis";

const ADMIN = `0x${"aa".repeat(20)}`;
const AUDITOR = "0x9999999999999999999999999999999999999999";
const QUOTE_TOKEN = "0x8888888888888888888888888888888888888888";
const INCOME_MANAGER = `0x${"bb".repeat(20)}`;
const FEED = `0x${"fe".repeat(20)}`;

test.describe("Setup - rwa-v4 factory", () => {
  mockOnly();

  async function reachDeployForm(page: import("@playwright/test").Page) {
    const setup = new AdminSetupPage(page);
    await setup.goto();
    await setup.createProfileFor({ tokenUnit: "gram", tokenDecimals: 18 });
    await expect(setup.createdProfileStatus()).toContainText("Persisted");
    for (const id of [
      "admin",
      "complianceOperator",
      "treasurer",
      "redemptionManager",
      "treasury",
    ]) {
      await page.locator(`#${id}`).fill(ADMIN);
    }
    await page.locator("#name").fill("Gold Bar Token");
    await page.locator("#symbol").fill("GOLD");
    await page.locator("#quoteToken").fill(QUOTE_TOKEN);
    await page.locator("#auditor").fill(AUDITOR);
    await page.locator("#incomeManager").fill(INCOME_MANAGER);
    return setup;
  }

  test("an oracle deployment broadcasts the dealing cut-off and both timeouts", async ({
    page,
    api,
  }) => {
    api.factoryVersion = "rwa-v4";
    const setup = await reachDeployForm(page);
    await page.locator("#strategyKind").selectOption("Oracle");
    await page.locator("#priceFeed").fill(FEED);
    await page.locator("#priceMaxAge").fill("3600");
    await page.locator("#dealingCutoff").fill("7200");
    await page.locator("#purchaseTimeout").fill("1209600");

    await setup.reviewDeployment();
    await expect(
      page.getByText("Dealing cut-off", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Purchase timeout", { exact: true }),
    ).toBeVisible();
    await setup.confirmDeploy();
    await expect(setup.deployedConfirmation()).toBeVisible();

    const deployTx = (await getMockSentTransactions(page)).find(
      (t) => t.to?.toLowerCase() === FACTORY_ADDRESS.toLowerCase(),
    );
    expect(deployTx, "a deploy tx was broadcast").toBeTruthy();
    const config = decodeFunctionData({
      abi: rwaFactoryV4Abi,
      data: deployTx!.data as `0x${string}`,
    }).args[0] as Record<string, unknown>;
    expect(config).toMatchObject({
      strategyKind: 1,
      dealingCutoff: 7200n,
      redemptionTimeout: 604_800n,
      purchaseTimeout: 1_209_600n,
    });
  });

  test("a cut-off that reaches a timeout blocks the review step", async ({
    page,
    api,
  }) => {
    api.factoryVersion = "rwa-v4";
    await reachDeployForm(page);
    await page.locator("#strategyKind").selectOption("Oracle");
    await page.locator("#priceFeed").fill(FEED);
    await page.locator("#priceMaxAge").fill("3600");
    await page.locator("#dealingCutoff").fill("604800");

    await expect(
      page.getByRole("button", { name: "Review deployment" }),
    ).toBeDisabled();
    await expect(
      page.getByText(/must be below both the redemption timeout/),
    ).toBeVisible();
  });
});
