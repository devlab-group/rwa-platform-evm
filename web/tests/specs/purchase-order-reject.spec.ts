import {
  decodeFunctionData,
  encodeFunctionData,
  encodeFunctionResult,
} from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminInventorySalesPage } from "../pages/AdminInventorySalesPage";
import { ADDRESSES } from "../fixtures/mock-api";
import { getMockSentTransactions, setMockCall } from "../fixtures/mock-wallet";
import { oracleStrategyAbi, vaultAbi } from "../../src/lib/abis";

const CREATED_AT = Math.floor(Date.now() / 1000) - 600;

const ORDER = {
  id: "5",
  buyer: "0x00000000000000000000000000000000000000b1",
  recipient: "0x00000000000000000000000000000000000000b2",
  tokenAmount: "3000000000000000000",
  maxQuoteAmount: "4000000",
  strategy: ADDRESSES.strategy,
  baseRound: "40",
  status: "Pending" as const,
  createdAt: CREATED_AT,
  timeoutAt: CREATED_AT + 604_800,
  requestTxHash: `0x${"ab".repeat(32)}`,
};

test.describe("Purchase orders - rejecting while paused", () => {
  mockOnly();

  async function openOrder(
    page: import("@playwright/test").Page,
    quote = 2_500_000n,
  ) {
    const sales = new AdminInventorySalesPage(page);
    await sales.goto();
    await sales.connectWallet();
    // The order as the Vault stores it, with the minimum recorded when it was requested (1 quote token)
    await setMockCall(
      page,
      encodeFunctionData({
        abi: vaultAbi,
        functionName: "getPurchase",
        args: [5n],
      }),
      {
        result: encodeFunctionResult({
          abi: vaultAbi,
          functionName: "getPurchase",
          result: {
            buyer: ORDER.buyer as `0x${string}`,
            createdAt: BigInt(CREATED_AT),
            status: 0,
            recipient: ORDER.recipient as `0x${string}`,
            strategy: ORDER.strategy as `0x${string}`,
            baseRound: 40n,
            tokenAmount: 3_000_000_000_000_000_000n,
            maxQuoteAmount: 4_000_000n,
            minPurchaseQuote: 1_000_000n,
          },
        }),
      },
    );
    // The round that prices the order, by default 2.5 quote tokens at round 42
    await setMockCall(
      page,
      encodeFunctionData({
        abi: oracleStrategyAbi,
        functionName: "quotePurchaseAfter",
        args: [3_000_000_000_000_000_000n, 40n, BigInt(CREATED_AT)],
      }),
      {
        result: encodeFunctionResult({
          abi: oracleStrategyAbi,
          functionName: "quotePurchaseAfter",
          result: [quote, 42n],
        }),
      },
    );
    await expect(sales.ordersSection()).toBeVisible();
    await sales.manageOrder("5");
    await expect(sales.orderDetail("5")).toBeVisible();
    return sales;
  }

  test("the treasurer rejects a Pending order from the connected wallet and gets the whole deposit back", async ({
    page,
    api,
  }) => {
    api.project.version = "rwa-v4";
    api.project.forwardPriced = true;
    api.project.paused = true;
    api.project.quoteDecimals = 6;
    api.purchaseOrders.push(ORDER);

    const sales = await openOrder(page);
    const detail = sales.orderDetail("5");
    // Settlement is refused while paused, but rejection is open to the treasurer
    await expect(
      detail.getByRole("button", { name: "Settle order" }),
    ).toBeDisabled();
    await expect(
      detail.getByText(
        /returns the buyer's whole deposit of 4 and closes the order as Rejected/,
      ),
    ).toBeVisible();

    await detail.getByRole("button", { name: "Reject order" }).click();

    await expect(detail.getByText(/Order rejected/)).toBeVisible();
    const sent = await getMockSentTransactions(page);
    expect(sent).toHaveLength(1);
    expect(sent[0].to?.toLowerCase()).toBe(ADDRESSES.vault.toLowerCase());
    expect(
      decodeFunctionData({
        abi: vaultAbi,
        data: sent[0].data as `0x${string}`,
      }),
    ).toEqual({
      functionName: "rejectPurchase",
      args: [5n],
    });
  });

  test("a round above the deposit warns that settling refunds it", async ({
    page,
    api,
  }) => {
    api.project.version = "rwa-v4";
    api.project.forwardPriced = true;
    api.project.paused = false;
    api.project.quoteDecimals = 6;
    api.purchaseOrders.push(ORDER);

    const sales = await openOrder(page, 5_000_000n);
    const detail = sales.orderDetail("5");
    await expect(
      detail.getByText(
        /Priced at round 42: 5, above the buyer's deposit of 4\. Settling will refund the whole deposit/,
      ),
    ).toBeVisible();
    await expect(detail.getByText(/Anyone can settle it/)).toHaveCount(0);
  });

  test("an unpaused project offers settle but no reject", async ({
    page,
    api,
  }) => {
    api.project.version = "rwa-v4";
    api.project.forwardPriced = true;
    api.project.paused = false;
    api.project.quoteDecimals = 6;
    api.purchaseOrders.push(ORDER);

    const sales = await openOrder(page);
    const detail = sales.orderDetail("5");
    await expect(detail.getByText(/Priced at round 42: 2.5/)).toBeVisible();
    await expect(
      detail.getByRole("button", { name: "Settle order" }),
    ).toBeEnabled();
    await expect(
      detail.getByRole("button", { name: "Reject order" }),
    ).toHaveCount(0);
    await expect(
      detail.getByText(/rejected only while the project is paused/),
    ).toBeVisible();
  });
});
