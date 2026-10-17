import { readFile } from "node:fs/promises";
import { decodeFunctionData, erc20Abi } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { ADDRESSES } from "../fixtures/mock-api";
import {
  getMockSentTransactions,
  MOCK_WALLET_ADDRESS,
} from "../fixtures/mock-wallet";
import { AdminIncomePage } from "../pages/AdminIncomePage";
import { incomeDistributorAbi } from "../../src/lib/abis";

const DISTRIBUTOR = `0x${"9a".repeat(20)}`;
const SAFE = "0x9a7F3dfDEeBb51eAbBbA3c8eCCf8cF2a983A1C72";
const DAY = 86_400;

test.describe("Income distribution", () => {
  mockOnly();

  test.beforeEach(({ api }) => {
    api.project.version = "rwa-v3";
    api.project.quoteDecimals = 6;
    api.project.addresses = {
      ...ADDRESSES,
      incomeDistributor: DISTRIBUTOR,
    };
    api.project.roles = {
      ...api.project.roles,
      INCOME_MANAGER_ROLE: [MOCK_WALLET_ADDRESS],
    };
  });

  test("the Income link shows for an rwa-v3 project only", async ({
    page,
    api,
  }) => {
    await page.goto("/admin/setup");
    await expect(page.getByRole("link", { name: "Income" })).toBeVisible();

    api.project.version = "rwa-v2";
    await page.goto("/admin/setup");
    await expect(page.getByRole("link", { name: "Security" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Income" })).toHaveCount(0);
  });

  test("an rwa-v2 project gets an explanation instead of the forms", async ({
    page,
    api,
  }) => {
    api.project.version = "rwa-v2";
    await new AdminIncomePage(page).goto();
    await expect(page.getByText(/needs an rwa-v3 project/)).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Prepare distribution" }),
    ).toHaveCount(0);
  });

  test("prepares a snapshot, then the wallet approves the exact total and creates the distribution", async ({
    page,
  }) => {
    const income = new AdminIncomePage(page);
    await income.goto();
    await income.connectWallet();

    await income.prepare("100", "1250.5");
    await expect(income.sendSection()).toBeVisible();
    await income
      .sendSection()
      .getByRole("button", { name: "Review and send" })
      .click();
    // The review shows the chain, addresses and amounts before any approval
    await expect(page.getByText("Confirm income distribution")).toBeVisible();
    await expect(income.sendSection().getByText(DISTRIBUTOR)).toBeVisible();
    await expect(
      income.sendSection().getByText(/1,250\.5 \(1250500000/),
    ).toBeVisible();
    await income
      .sendSection()
      .getByRole("button", { name: "Approve and create" })
      .click();

    await expect(page.getByText(/Distribution created/)).toBeVisible();
    const sent = await getMockSentTransactions(page);
    expect(sent).toHaveLength(2);
    expect(sent[0].to?.toLowerCase()).toBe(ADDRESSES.quoteToken.toLowerCase());
    expect(
      decodeFunctionData({
        abi: erc20Abi,
        data: sent[0].data as `0x${string}`,
      }),
    ).toMatchObject({
      functionName: "approve",
      args: [
        expect.stringMatching(new RegExp(DISTRIBUTOR, "i")),
        1_250_500_000n,
      ],
    });
    expect(sent[1].to?.toLowerCase()).toBe(DISTRIBUTOR.toLowerCase());
    const create = decodeFunctionData({
      abi: incomeDistributorAbi,
      data: sent[1].data as `0x${string}`,
    });
    expect(create.functionName).toBe("createDistribution");
    expect(create.args?.[1]).toBe(1_250_500_000n);
    expect(create.args?.[2]).toBe(100n);
    expect(create.args?.[4]).toBe("ipfs://bafymocksnapshot");
  });

  test("a deadline under 7 days and 1 hour is refused before any request", async ({
    page,
  }) => {
    const income = new AdminIncomePage(page);
    await income.goto();
    const soon = new Date(Date.now() + 7 * DAY * 1000);
    const pad = (n: number) => String(n).padStart(2, "0");
    await income
      .prepareSection()
      .getByLabel("Claim deadline")
      .fill(
        `${soon.getFullYear()}-${pad(soon.getMonth() + 1)}-${pad(soon.getDate())}T${pad(soon.getHours())}:${pad(soon.getMinutes())}`,
      );
    await income.prepare("100", "10");
    await expect(
      income.prepareSection().getByText(/at least 7 days and 1 hour/),
    ).toBeVisible();
    await expect(income.sendSection()).toHaveCount(0);
  });

  test("a Safe holding INCOME_MANAGER_ROLE saves an approve + createDistribution batch", async ({
    page,
    api,
  }) => {
    api.project.roles = { ...api.project.roles, INCOME_MANAGER_ROLE: [SAFE] };
    const income = new AdminIncomePage(page);
    await income.goto();
    await income.connectWallet();
    await income.prepare("100", "10");

    await income
      .sendSection()
      .getByLabel(/Safe holding INCOME_MANAGER_ROLE/)
      .selectOption(SAFE);
    await income
      .sendSection()
      .getByRole("button", { name: "Review and send" })
      .click();
    const downloading = page.waitForEvent("download");
    await income
      .sendSection()
      .getByRole("button", { name: "Save Safe batch" })
      .click();
    const download = await downloading;

    const batch = JSON.parse(await readFile(await download.path(), "utf8"));
    expect(batch.meta.createdFromSafeAddress).toBe(SAFE);
    expect(
      batch.transactions.map((t: { to: string }) => t.to.toLowerCase()),
    ).toEqual([ADDRESSES.quoteToken.toLowerCase(), DISTRIBUTOR.toLowerCase()]);
    expect(await getMockSentTransactions(page)).toHaveLength(0);
  });

  test("lists distributions and extends an active deadline from the manager wallet", async ({
    page,
    api,
  }) => {
    const deadline = Math.floor(Date.now() / 1000) + 10 * DAY;
    api.distributions.push({
      preparationId: "prep-seed",
      status: "Active",
      distributionId: "4",
      merkleRoot: `0x${"ab".repeat(32)}`,
      total: "5000000",
      recordBlock: 90,
      claimDeadline: deadline,
      snapshotURI: "https://gw.example/snap.json",
      snapshotSha256: `0x${"cd".repeat(32)}`,
      eligibleSupply: "1",
      distributed: "5000000",
      holderCount: 7,
      claimed: "1000000",
      createdAt: new Date().toISOString(),
    });
    const income = new AdminIncomePage(page);
    await income.goto();
    await income.connectWallet();

    const row = income.row("Active");
    await expect(row).toContainText("5");
    await expect(
      row.getByRole("link", { name: "https://gw.example/snap.json" }),
    ).toBeVisible();
    await row
      .getByRole("button", { name: /Extend deadline of distribution 4/ })
      .click();

    const later = new Date((deadline + 5 * DAY) * 1000);
    const pad = (n: number) => String(n).padStart(2, "0");
    await income
      .extendSection()
      .getByLabel("New claim deadline")
      .fill(
        `${later.getFullYear()}-${pad(later.getMonth() + 1)}-${pad(later.getDate())}T${pad(later.getHours())}:${pad(later.getMinutes())}`,
      );
    await income
      .extendSection()
      .getByRole("button", { name: "Extend deadline" })
      .click();

    await expect(page.getByText("Claim deadline extended.")).toBeVisible();
    const [tx] = await getMockSentTransactions(page);
    expect(tx.to?.toLowerCase()).toBe(DISTRIBUTOR.toLowerCase());
    const extend = decodeFunctionData({
      abi: incomeDistributorAbi,
      data: tx.data as `0x${string}`,
    });
    expect(extend.functionName).toBe("extendClaimDeadline");
    // Distribution 4, moved five days later (the picker truncates to the minute)
    const [id, newDeadline] = extend.args as [bigint, bigint];
    expect(id).toBe(4n);
    expect(Math.abs(Number(newDeadline) - (deadline + 5 * DAY))).toBeLessThan(120);
  });

  test("reclaim is offered only after the deadline and is disabled while paused", async ({
    page,
    api,
  }) => {
    const base = {
      merkleRoot: `0x${"ab".repeat(32)}`,
      total: "5000000",
      recordBlock: 90,
      snapshotURI: "ipfs://x",
      snapshotSha256: `0x${"cd".repeat(32)}`,
      eligibleSupply: "1",
      distributed: "5000000",
      holderCount: 7,
      createdAt: new Date().toISOString(),
    };
    api.distributions.push(
      {
        ...base,
        preparationId: "p-open",
        status: "Active",
        distributionId: "1",
        claimDeadline: Math.floor(Date.now() / 1000) + 9 * DAY,
      },
      {
        ...base,
        preparationId: "p-done",
        status: "Expired",
        distributionId: "2",
        claimDeadline: Math.floor(Date.now() / 1000) - DAY,
      },
    );
    const income = new AdminIncomePage(page);
    await income.goto();
    await income.connectWallet();

    await expect(
      page.getByRole("button", {
        name: /Reclaim unclaimed income of distribution 1/,
      }),
    ).toBeDisabled();
    const ready = page.getByRole("button", {
      name: /Reclaim unclaimed income of distribution 2/,
    });
    await expect(ready).toBeEnabled();
    await ready.click();
    await expect
      .poll(async () => (await getMockSentTransactions(page)).length)
      .toBe(1);
    const [tx] = await getMockSentTransactions(page);
    expect(
      decodeFunctionData({
        abi: incomeDistributorAbi,
        data: tx.data as `0x${string}`,
      }),
    ).toMatchObject({ functionName: "reclaimUnclaimed", args: [2n] });

    api.project.paused = true;
    await page.reload();
    await expect(
      page.getByRole("button", {
        name: /Reclaim unclaimed income of distribution 2/,
      }),
    ).toBeDisabled();
  });
});
