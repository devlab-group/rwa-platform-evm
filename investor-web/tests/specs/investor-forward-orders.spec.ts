import { decodeFunctionData } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { allowTheWallet, makeForward } from "../fixtures/helpers";
import { ADDRESSES } from "../fixtures/mock-api";
import {
  getMockSentTransactions,
  MOCK_WALLET_ADDRESS,
  setMockRound,
} from "../fixtures/mock-wallet";
import { vaultAbi } from "../../src/lib/abis";
import { InvestorPage } from "../pages/InvestorPage";

const nowSeconds = () => Math.floor(Date.now() / 1000);

test.describe("Investor - forward-priced orders", () => {
  mockOnly();

  test("places an order, sees it priced, then settles it", async ({
    page,
    api,
  }) => {
    makeForward(api);
    allowTheWallet(api);
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    // The notice states the pricing rule before anything is confirmed
    await investor.previewBuy("1");
    const preview = page.getByRole("region", { name: "Purchase preview" });
    await expect(preview).toContainText(
      "published at least 2 hours after you place the order",
    );
    await expect(preview).toContainText(
      "the unused part comes back when the order settles",
    );
    await expect(preview).toContainText(
      "if the price exceeds the deposit, the inventory runs out or the recipient stops being eligible",
    );

    await investor.approveBuy();
    await expect(
      investor.buySection.getByRole("button", { name: "2. Place order" }),
    ).toBeEnabled();
    await investor.placeOrder();
    await expect(page.getByText(/Order submitted:/)).toBeVisible();

    // The Vault gets requestPurchase with the slippage-ceiled estimate as the deposit
    const sent = (await getMockSentTransactions(page)).at(-1);
    expect(sent?.to?.toLowerCase()).toBe(ADDRESSES.vault.toLowerCase());
    const decoded = decodeFunctionData({
      abi: vaultAbi,
      data: sent!.data as `0x${string}`,
    });
    expect(decoded.functionName).toBe("requestPurchase");
    // 1 token at 18 decimals; the mock quotes 1000000, plus 50 bps
    expect(decoded.args?.[0]).toBe(10n ** 18n);
    expect(decoded.args?.[1]).toBe(1_005_000n);

    // The server indexes the Vault event; no round has landed yet
    api.orders.push({
      id: "7",
      buyer: MOCK_WALLET_ADDRESS,
      recipient: MOCK_WALLET_ADDRESS,
      tokenAmount: (decoded.args?.[0] as bigint).toString(),
      maxQuoteAmount: (decoded.args?.[1] as bigint).toString(),
      strategy: ADDRESSES.strategy,
      baseRound: "100",
      status: "Pending",
      createdAt: nowSeconds(),
      timeoutAt: nowSeconds() + 14 * 86_400,
      requestTxHash: `0x${"01".repeat(32)}`,
    });
    await investor.refreshOrders();
    await expect(investor.ordersSection).toContainText("Waiting for a price.");
    await expect(
      investor.ordersSection.getByRole("button", { name: "Settle" }),
    ).toHaveCount(0);
    await expect(
      investor.ordersSection.getByRole("button", { name: "Cancel order" }),
    ).toHaveCount(0);

    // The feed publishes the pricing round
    await setMockRound(page, true);
    await investor.refreshOrders();
    await expect(investor.ordersSection).toContainText(
      "Priced at 0.99 quote tokens. Settling pays that and returns 0.015 of your deposit.",
    );

    await investor.ordersSection
      .getByRole("button", { name: "Settle" })
      .click();
    await expect(investor.ordersSection).toContainText("Settle submitted:");
    const settle = (await getMockSentTransactions(page)).at(-1);
    expect(settle?.to?.toLowerCase()).toBe(ADDRESSES.vault.toLowerCase());
    const settled = decodeFunctionData({
      abi: vaultAbi,
      data: settle!.data as `0x${string}`,
    });
    expect(settled.functionName).toBe("settlePurchase");
    expect(settled.args).toEqual([7n]);
  });

  test("offers Cancel, not Settle, once the timeout passed with no price", async ({
    page,
    api,
  }) => {
    makeForward(api);
    allowTheWallet(api);
    api.orders.push({
      id: "3",
      buyer: MOCK_WALLET_ADDRESS,
      recipient: MOCK_WALLET_ADDRESS,
      tokenAmount: (10n ** 18n).toString(),
      maxQuoteAmount: "1005000",
      strategy: ADDRESSES.strategy,
      baseRound: "100",
      status: "Pending",
      createdAt: nowSeconds() - 15 * 86_400,
      timeoutAt: nowSeconds() - 86_400,
      requestTxHash: `0x${"02".repeat(32)}`,
    });
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await expect(investor.ordersSection).toContainText("Waiting for a price.");
    await expect(
      investor.ordersSection.getByRole("button", { name: "Settle" }),
    ).toHaveCount(0);
    await investor.ordersSection
      .getByRole("button", { name: "Cancel order" })
      .click();

    await expect(investor.ordersSection).toContainText("Cancel submitted:");
    const cancel = (await getMockSentTransactions(page)).at(-1);
    const decoded = decodeFunctionData({
      abi: vaultAbi,
      data: cancel!.data as `0x${string}`,
    });
    expect(decoded.functionName).toBe("cancelPurchase");
    expect(decoded.args).toEqual([3n]);
  });

  test("explains a buyer who is not Allowed instead of offering Cancel", async ({
    page,
    api,
  }) => {
    makeForward(api);
    // The default mock registry does not list the wallet, so the server reports it ineligible
    api.orders.push({
      id: "4",
      buyer: MOCK_WALLET_ADDRESS,
      recipient: MOCK_WALLET_ADDRESS,
      tokenAmount: (10n ** 18n).toString(),
      maxQuoteAmount: "1005000",
      strategy: ADDRESSES.strategy,
      baseRound: "100",
      status: "Pending",
      createdAt: nowSeconds() - 15 * 86_400,
      timeoutAt: nowSeconds() - 86_400,
      requestTxHash: `0x${"03".repeat(32)}`,
    });
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await expect(investor.ordersSection).toContainText(
      "The buyer wallet is not Allowed in the compliance registry",
    );
    await expect(
      investor.ordersSection.getByRole("button", {
        name: /Settle|Cancel order/,
      }),
    ).toHaveCount(0);
  });

  test("an instant project shows no order section and keeps the plain Buy button", async ({
    page,
  }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await investor.previewBuy("1");
    await expect(
      investor.buySection.getByRole("button", { name: "2. Buy" }),
    ).toBeVisible();
    await expect(
      investor.buySection.getByRole("button", { name: "2. Place order" }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("heading", { name: "Purchase orders" }),
    ).toHaveCount(0);
  });
});
