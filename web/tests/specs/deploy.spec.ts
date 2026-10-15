import { decodeFunctionData, keccak256, toBytes } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminSetupPage } from "../pages/AdminSetupPage";
import { FACTORY_ADDRESS } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";
import { rwaFactoryAbi } from "../../src/lib/abis";

// 20-byte addresses; the deploy ABI-encodes them as `address`, so a wrong
// length throws before broadcast
const ADMIN = `0x${"aa".repeat(20)}`;
const AUDITOR = "0x9999999999999999999999999999999999999999";
const QUOTE_TOKEN = "0x8888888888888888888888888888888888888888";

test.describe("Setup - profile then deploy", () => {
  mockOnly();

  test("goes from no project through a persisted profile to a wallet-broadcast RWAFactory.deploy", async ({
    page,
  }) => {
    const setup = new AdminSetupPage(page);
    await setup.goto();

    // Deployment is locked until a profile is stored
    await expect(
      page.getByText(/Create the Asset Profile above/),
    ).toBeVisible();

    await setup.createProfileFor({
      tokenUnit: "gram",
      tokenDecimals: 18,
    });
    await expect(setup.createdProfileStatus()).toContainText("Persisted");
    await expect(setup.createdProfileStatus()).toContainText("gram");
    // projectId comes from the server config, not from the operator; capture it
    // to assert the on-chain projectId and the confirmation against it
    const projectId = await setup.generatedProjectId();
    expect(projectId).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i,
    );
    await expect(setup.createdProfileStatus()).toContainText(projectId);

    // The deploy form shows the derived project ID, decimals and digest, and
    // every role field RWAFactory requires
    await setup.fillDeployForm({
      name: "Gold Bar Token",
      symbol: "GOLD",
      quoteToken: QUOTE_TOKEN,
      admin: ADMIN,
      auditor: AUDITOR,
    });

    const reviewButton = page.getByRole("button", {
      name: "Review deployment",
    });
    await expect(reviewButton).toBeEnabled();

    await reviewButton.click();
    await expect(page.getByText("Confirm deployment")).toBeVisible();
    await setup.confirmDeploy();

    // The admin's wallet broadcasts the deployment to the RWAFactory address
    // from GET /api/v1/config; the server only observes ProjectDeployed
    await expect(setup.deployedConfirmation()).toBeVisible();
    await expect(setup.deployedConfirmation()).toContainText(projectId);

    const txs = await getMockSentTransactions(page);
    const deployTx = txs.find(
      (t) => t.to?.toLowerCase() === FACTORY_ADDRESS.toLowerCase(),
    );
    expect(deployTx, "a deploy tx to the RWAFactory was broadcast").toBeTruthy();

    const { functionName, args } = decodeFunctionData({
      abi: rwaFactoryAbi,
      data: deployTx!.data as `0x${string}`,
    });
    expect(functionName).toBe("deploy");
    const config = args[0] as Record<string, unknown>;
    // projectId is the keccak256 of the profile's UUID string; the server
    // derives the on-chain bytes32 the same way
    expect(config.projectId).toBe(keccak256(toBytes(projectId)));
    // decimals comes from the stored profile
    expect(config.decimals).toBe(18);
    // Every RWAFactory-required role address is present and non-zero.
    for (const field of [
      "quoteToken",
      "admin",
      "auditor",
      "complianceOperator",
      "pricer",
      "treasurer",
      "redemptionManager",
      "treasury",
    ]) {
      expect(BigInt(config[field] as string), `${field} must be set`).not.toBe(
        0n,
      );
    }
  });

  test("Review deployment stays disabled until every required role field is filled", async ({
    page,
  }) => {
    const setup = new AdminSetupPage(page);
    await setup.goto();

    await setup.createProfileFor({
      tokenUnit: "gram",
      tokenDecimals: 18,
    });

    const reviewButton = page.getByRole("button", {
      name: "Review deployment",
    });
    await expect(reviewButton).toBeDisabled();

    // Name, symbol, quote token, admin and auditor are not enough; the
    // compliance operator, pricer, treasurer, redemption manager and treasury
    // fields must be filled too
    await page.locator("#name").fill("Gold Bar Token");
    await page.locator("#symbol").fill("GOLD");
    await page.locator("#quoteToken").fill(QUOTE_TOKEN);
    await page.locator("#purchasePrice").fill("1000000");
    await page.locator("#redemptionPrice").fill("950000");
    await page.locator("#admin").fill(ADMIN);
    await page.locator("#auditor").fill(AUDITOR);
    await expect(reviewButton).toBeDisabled();

    await page.locator("#complianceOperator").fill(ADMIN);
    await page.locator("#pricer").fill(ADMIN);
    await page.locator("#treasurer").fill(ADMIN);
    await page.locator("#redemptionManager").fill(ADMIN);
    await expect(reviewButton).toBeDisabled();

    await page.locator("#treasury").fill(ADMIN);
    await expect(reviewButton).toBeEnabled();
  });

  test("a reload repopulates the persisted profile instead of forcing re-creation (create-once)", async ({
    page,
  }) => {
    const setup = new AdminSetupPage(page);
    await setup.goto();
    await setup.createProfileFor({
      tokenUnit: "gram",
      tokenDecimals: 18,
    });
    await expect(setup.createdProfileStatus()).toContainText("Persisted");
    const projectId = await setup.generatedProjectId();

    // Reload must restore GET /api/v1/profile, prevent a second profile creation and
    // keep Deployment unlocked
    await setup.goto();
    await expect(setup.createdProfileStatus()).toContainText("Persisted");
    await expect(page.getByLabel("Stored Asset Profile JSON")).toHaveValue(
      new RegExp(projectId),
    );
    await expect(
      page.getByRole("button", { name: "Create & persist profile" }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Review deployment" }),
    ).toBeVisible();
  });
});
