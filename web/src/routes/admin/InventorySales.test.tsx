import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { decodeFunctionData, encodeErrorResult } from "viem";
import { InventorySales } from "./InventorySales";
import { oracleStrategyErrorsAbi, vaultAbi } from "../../lib/abis";
import { api } from "../../lib/client";
import { exportSafeBatch } from "../../lib/safeBatch";
import {
  readOrderMinQuote,
  readOrderPricing,
  readPreviewBuy,
  sendRejectPurchase,
  sendSettlePurchase,
  sendWithdrawProceeds,
  type OrderPricing,
} from "../../lib/wallet";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";

// The role-bearing fake wallet enables direct withdrawal; contracts enforce TREASURER_ROLE
// authorization
const CONNECTED = "0x1111111111111111111111111111111111111111";

// Assert separate scales: RWA previews use 6 decimals; quote withdrawals use 18
vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: vi.fn(),
      getInventory: vi.fn(),
      listPurchases: vi.fn(),
      listPurchaseOrders: vi.fn(),
      getPurchaseOrder: vi.fn(),
    },
  };
});

// Spy on the Safe batch export without downloading a browser file
vi.mock("../../lib/safeBatch", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/safeBatch")>();
  return { ...actual, exportSafeBatch: vi.fn(() => "Saved batch.") };
});

// Stub 18-decimal quote data versus 6-decimal RWA, previews and writes to check conversions without
// a chain
vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Decimals: vi.fn().mockResolvedValue(18),
    readPreviewBuy: vi.fn().mockResolvedValue(2000000n),
    readOrderPricing: vi.fn(),
    readOrderMinQuote: vi.fn(),
    sendSettlePurchase: vi.fn(),
    sendRejectPurchase: vi.fn(),
    sendWithdrawProceeds: vi.fn().mockResolvedValue("0xhash"),
    waitForTxReceipt: vi.fn().mockResolvedValue(undefined),
  };
});

const PROJECT = {
  chainId: 31337,
  decimals: 6,
  tokenUnit: "RWA",
  addresses: {
    token: "0x0000000000000000000000000000000000000001",
    quoteToken: "0x0000000000000000000000000000000000000002",
    vault: "0x0000000000000000000000000000000000000003",
  },
  roles: { TREASURER_ROLE: [CONNECTED] },
};

describe("InventorySales amount conversion", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: CONNECTED, chainId: 31337 });
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    vi.mocked(api.getInventory).mockReset().mockResolvedValue({});
    vi.mocked(readPreviewBuy).mockClear().mockResolvedValue(2000000n);
    vi.mocked(sendWithdrawProceeds).mockClear().mockResolvedValue("0xhash");
    vi.mocked(api.listPurchases).mockReset().mockResolvedValue({ items: [] });
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("reads the buy quote off the vault using the RWA token decimals", async () => {
    renderWithWallet(<InventorySales />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
      target: { value: "1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Preview quote" }));

    // Preview 1 whole RWA as 1000000n on the project Vault/chain
    await waitFor(() =>
      expect(readPreviewBuy).toHaveBeenCalledWith(
        31337,
        PROJECT.addresses.vault,
        1000000n,
      ),
    );
  });

  it("broadcasts Vault.withdrawProceeds scaled by the QUOTE token decimals", async () => {
    renderWithWallet(<InventorySales />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(
      screen.getByLabelText("Amount (whole quote-token units)"),
      { target: { value: "1" } },
    );
    fireEvent.click(screen.getByRole("button", { name: "Withdraw" }));

    // Withdraw 1 whole quote unit as 1000000000000000000n, independent of RWA decimals
    await waitFor(() =>
      expect(sendWithdrawProceeds).toHaveBeenCalledWith(
        31337,
        CONNECTED,
        PROJECT.addresses.vault,
        1000000000000000000n,
      ),
    );
  });

  it("blocks a withdrawal that exceeds the vault's available quote balance", async () => {
    // Vault holds 5 whole quote tokens (18 decimals).
    vi.mocked(api.getInventory)
      .mockReset()
      .mockResolvedValue({ quoteBalance: "5000000000000000000" });
    renderWithWallet(<InventorySales />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(
      screen.getByLabelText("Amount (whole quote-token units)"),
      { target: { value: "10" } },
    );

    // After resolving 18 decimals, 10 > 5 disables withdrawal and shows an error
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Withdraw" })).toBeDisabled(),
    );
    expect(
      screen.getByText(/exceeds the vault's available quote balance/i),
    ).toBeInTheDocument();
    expect(sendWithdrawProceeds).not.toHaveBeenCalled();
  });

  it("hides the withdrawal form from a wallet without TREASURER_ROLE on the Vault", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      roles: { TREASURER_ROLE: ["0x9999999999999999999999999999999999999999"] },
    });
    renderWithWallet(<InventorySales />, { connected: true });

    expect(
      await screen.findByText(/withdraw treasury proceeds/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Withdraw" })).toBeNull();
    expect(sendWithdrawProceeds).not.toHaveBeenCalled();
  });

  it("explains a sequencer outage in the quote preview instead of a raw revert", async () => {
    vi.mocked(readPreviewBuy).mockRejectedValue(
      Object.assign(new Error("execution reverted"), {
        data: encodeErrorResult({
          abi: oracleStrategyErrorsAbi,
          errorName: "SequencerDown",
        }),
      }),
    );
    renderWithWallet(<InventorySales />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    fireEvent.change(await screen.findByLabelText(/RWA token amount/), {
      target: { value: "1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Preview quote" }));
    expect(
      await screen.findByText(
        /sequencer uptime feed reports the sequencer is down/,
      ),
    ).toBeInTheDocument();
  });

  // A stale oracle leaves the prices out of the inventory; the operator needs the reason, not "-"
  it("shows why prices are missing when the oracle strategy reports a price error", async () => {
    vi.mocked(api.getInventory).mockResolvedValue({
      inventory: "500",
      quoteBalance: "900",
      priceError: "StalePrice",
    });
    renderWithWallet(<InventorySales />, { connected: true });

    expect(
      await screen.findByText(/the oracle strategy reports StalePrice/),
    ).toBeVisible();
  });

  it("shows no price status while the strategy reports prices", async () => {
    vi.mocked(api.getInventory).mockResolvedValue({
      inventory: "500",
      quoteBalance: "900",
      purchasePrice: "1000000",
      redemptionPrice: "990000",
    });
    renderWithWallet(<InventorySales />, { connected: true });

    await screen.findByText("Purchase price");
    expect(screen.queryByText("Price status")).toBeNull();
  });
});

// rwa-v4 projects: the Vault holds buyers' deposits until forward-priced orders settle
describe("InventorySales on a forward-priced project", () => {
  const VAULT = PROJECT.addresses.vault;
  const STRATEGY = "0x0000000000000000000000000000000000000060";
  const SAFE = "0x00000000000000000000000000000000000000f1";
  const FORWARD_PROJECT = {
    ...PROJECT,
    version: "rwa-v4",
    forwardPriced: true,
    paused: false,
    quoteDecimals: 18,
    oracle: { dealingCutoff: 7200 },
  };
  const ORDER = {
    id: "5",
    buyer: "0x00000000000000000000000000000000000000b1",
    recipient: "0x00000000000000000000000000000000000000b2",
    tokenAmount: "3000000",
    maxQuoteAmount: "4000000000000000000",
    strategy: STRATEGY,
    baseRound: "40",
    status: "Pending" as const,
    createdAt: 1_800_000_000,
    timeoutAt: 1_800_604_800,
    requestTxHash: "0xreq",
  };
  const priced: OrderPricing = {
    state: "priced",
    quoteAmount: 2_500_000_000_000_000_000n,
    roundId: 42n,
  };
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: CONNECTED, chainId: 31337 });
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue(FORWARD_PROJECT as never);
    vi.mocked(api.getInventory).mockReset().mockResolvedValue({});
    vi.mocked(api.listPurchases).mockReset().mockResolvedValue({ items: [] });
    vi.mocked(api.listPurchaseOrders)
      .mockReset()
      .mockResolvedValue({ items: [ORDER] });
    vi.mocked(api.getPurchaseOrder).mockReset().mockResolvedValue(ORDER);
    vi.mocked(readOrderPricing).mockReset().mockResolvedValue(priced);
    // The order minimum recorded at request time: 1 whole quote token
    vi.mocked(readOrderMinQuote)
      .mockReset()
      .mockResolvedValue(1_000_000_000_000_000_000n);
    vi.mocked(sendSettlePurchase).mockReset().mockResolvedValue("0xsettle");
    vi.mocked(sendRejectPurchase).mockReset().mockResolvedValue("0xreject");
    vi.mocked(sendWithdrawProceeds).mockReset().mockResolvedValue("0xhash");
    vi.mocked(exportSafeBatch).mockClear();
  });

  afterEach(() => {
    wallet.uninstall();
  });

  async function openOrder(order = ORDER) {
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order] });
    vi.mocked(api.getPurchaseOrder).mockResolvedValue(order);
    renderWithWallet(<InventorySales />, { connected: true });
    fireEvent.click(await screen.findByRole("button", { name: "Manage" }));
    await screen.findByRole("heading", { name: `Order ${order.id}` });
  }

  describe("withdrawal bound", () => {
    // 5 whole quote tokens in the Vault, 2 held for open orders: 3 can leave
    beforeEach(() => {
      vi.mocked(api.getInventory).mockResolvedValue({
        quoteBalance: "5000000000000000000",
        pendingPurchaseQuote: "2000000000000000000",
      });
    });

    const type = (value: string) =>
      fireEvent.change(
        screen.getByLabelText("Amount (whole quote-token units)"),
        {
          target: { value },
        },
      );

    it("shows the balance, the held deposits and what is withdrawable", async () => {
      renderWithWallet(<InventorySales />, { connected: true });
      await screen.findByText("Held for open purchase orders");
      const inventory = screen
        .getByText("Withdrawable")
        .closest("dl") as HTMLElement;
      expect(
        within(inventory).getByText("Quote balance").nextSibling,
      ).toHaveTextContent("5");
      expect(
        within(inventory).getByText("Held for open purchase orders")
          .nextSibling,
      ).toHaveTextContent("2");
      expect(
        within(inventory).getByText("Withdrawable").nextSibling,
      ).toHaveTextContent("3");
      expect(
        await screen.findByText(
          /Available in vault: 3 \(balance 5 minus 2 held for open purchase orders\)/,
        ),
      ).toBeInTheDocument();
    });

    it("blocks an amount above balance minus deposits, even though it is below the balance", async () => {
      renderWithWallet(<InventorySales />, { connected: true });
      await screen.findByText("Held for open purchase orders");
      type("3.000000000000000001");

      await waitFor(() =>
        expect(screen.getByRole("button", { name: "Withdraw" })).toBeDisabled(),
      );
      expect(
        screen.getByText(/exceeds the vault's available quote balance/i),
      ).toBeInTheDocument();
      expect(sendWithdrawProceeds).not.toHaveBeenCalled();
    });

    it("allows exactly the withdrawable amount and sends it", async () => {
      renderWithWallet(<InventorySales />, { connected: true });
      await screen.findByText("Held for open purchase orders");
      type("3");

      await waitFor(() =>
        expect(screen.getByRole("button", { name: "Withdraw" })).toBeEnabled(),
      );
      fireEvent.click(screen.getByRole("button", { name: "Withdraw" }));
      await waitFor(() =>
        expect(sendWithdrawProceeds).toHaveBeenCalledWith(
          31337,
          CONNECTED,
          VAULT,
          3_000_000_000_000_000_000n,
        ),
      );
    });

    it("bounds by the whole balance on a Vault that reports no held deposits", async () => {
      vi.mocked(api.getInventory).mockResolvedValue({
        quoteBalance: "5000000000000000000",
      });
      renderWithWallet(<InventorySales />, { connected: true });
      await screen.findByText("Quote balance");
      expect(screen.queryByText("Withdrawable")).toBeNull();
      type("5");
      await waitFor(() =>
        expect(screen.getByRole("button", { name: "Withdraw" })).toBeEnabled(),
      );
    });
  });

  describe("no Vault.buy on a forward-priced project", () => {
    it("labels the quote preview and the prices as indicative, and explains orders", async () => {
      vi.mocked(api.getInventory).mockResolvedValue({
        purchasePrice: "1000000",
        redemptionPrice: "950000",
      });
      renderWithWallet(<InventorySales />, { connected: true });

      expect(
        await screen.findByRole("heading", {
          name: "Indicative purchase quote",
        }),
      ).toBeInTheDocument();
      expect(screen.getByText(/Vault\.buy is disabled/)).toBeInTheDocument();
      expect(
        screen.getByText(/published at least 7200 s after the order/),
      ).toBeInTheDocument();
      expect(
        screen.getByText("Latest purchase price (indicative)"),
      ).toBeInTheDocument();
      expect(
        screen.getByText("Latest redemption price (indicative)"),
      ).toBeInTheDocument();
      expect(
        screen.queryByRole("heading", { name: "Purchase quote preview" }),
      ).toBeNull();
    });

    it("says orders are still accepted while the feed is unavailable", async () => {
      vi.mocked(api.getInventory).mockResolvedValue({
        priceError: "StalePrice",
      });
      renderWithWallet(<InventorySales />, { connected: true });
      expect(
        await screen.findByText(
          /orders and redemption requests are still accepted and price at a later round/,
        ),
      ).toBeInTheDocument();
    });

    it("keeps the instant wording on a project that is not forward priced", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...PROJECT,
        version: "rwa-v3",
      } as never);
      vi.mocked(api.getInventory).mockResolvedValue({
        purchasePrice: "1",
        priceError: "StalePrice",
      });
      renderWithWallet(<InventorySales />, { connected: true });

      expect(
        await screen.findByRole("heading", { name: "Purchase quote preview" }),
      ).toBeInTheDocument();
      expect(screen.getByText("Purchase price")).toBeInTheDocument();
      expect(
        screen.getByText(
          /Purchases and redemption requests revert until the feed recovers/,
        ),
      ).toBeInTheDocument();
      expect(screen.queryByText(/Vault\.buy is disabled/)).toBeNull();
    });
  });

  describe("purchase orders table", () => {
    it("is absent, and not requested, on a project that is neither rwa-v4 nor forward priced", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...PROJECT,
        version: "rwa-v3",
      } as never);
      renderWithWallet(<InventorySales />, { connected: true });
      await screen.findByRole("heading", { name: "Purchase history" });
      expect(
        screen.queryByRole("heading", { name: "Purchase orders" }),
      ).toBeNull();
      expect(api.listPurchaseOrders).not.toHaveBeenCalled();
    });

    it("still lists orders on an rwa-v4 project whose strategy is no longer forward priced", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD_PROJECT,
        forwardPriced: false,
      } as never);
      renderWithWallet(<InventorySales />, { connected: true });
      expect(
        await screen.findByRole("heading", { name: "Purchase orders" }),
      ).toBeInTheDocument();
      expect(await screen.findByText("5")).toBeInTheDocument();
    });

    it("filters by status through the API", async () => {
      renderWithWallet(<InventorySales />, { connected: true });
      await screen.findByText("5");
      expect(api.listPurchaseOrders).toHaveBeenLastCalledWith(
        expect.objectContaining({ status: undefined }),
      );

      fireEvent.change(screen.getByLabelText("Filter orders by status"), {
        target: { value: "Refunded" },
      });
      await waitFor(() =>
        expect(api.listPurchaseOrders).toHaveBeenLastCalledWith(
          expect.objectContaining({ status: "Refunded" }),
        ),
      );
    });

    it("loads the next page with the cursor from the previous one", async () => {
      vi.mocked(api.listPurchaseOrders)
        .mockResolvedValueOnce({
          items: [ORDER],
          totalCount: 2,
          nextCursor: "cursor-2",
        })
        .mockResolvedValueOnce({
          items: [{ ...ORDER, id: "6" }],
          totalCount: 2,
        });
      renderWithWallet(<InventorySales />, { connected: true });
      await screen.findByText(/Showing 1 of 2/);

      fireEvent.click(screen.getByRole("button", { name: "Load more" }));

      expect(await screen.findByText("6")).toBeInTheDocument();
      expect(api.listPurchaseOrders).toHaveBeenLastCalledWith(
        expect.objectContaining({ cursor: "cursor-2" }),
      );
      expect(screen.getByText("5")).toBeInTheDocument();
    });

    it("reads no price for the table rows, so the table needs no wallet", async () => {
      renderWithWallet(<InventorySales />, { connected: true });
      const row = (await screen.findByText("5")).closest("tr") as HTMLElement;
      expect(row).toHaveTextContent("Read in Manage");
      expect(readOrderPricing).not.toHaveBeenCalled();
      expect(readOrderMinQuote).not.toHaveBeenCalled();
    });

    it("lists orders on a page with no injected wallet", async () => {
      wallet.uninstall();
      renderWithWallet(<InventorySales />);
      expect(await screen.findByText("5")).toBeInTheDocument();
      expect(screen.queryByText("Price not read")).toBeNull();
    });

    it("shows the priced round and the refund of a closed order", async () => {
      vi.mocked(api.listPurchaseOrders).mockResolvedValue({
        items: [
          {
            ...ORDER,
            status: "Refunded" as const,
            pricedRound: "42",
            quoteAmount: "5000000000000000000",
            refund: "4000000000000000000",
          },
        ],
      });
      renderWithWallet(<InventorySales />, { connected: true });
      const row = (await screen.findByText("5")).closest("tr") as HTMLElement;
      expect(row).toHaveTextContent("Round 42, refund 4");
      expect(readOrderPricing).not.toHaveBeenCalled();
    });
  });

  describe("settle", () => {
    it("settles from a wallet that holds no role, and refreshes the list", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD_PROJECT,
        roles: {
          TREASURER_ROLE: ["0x9999999999999999999999999999999999999999"],
        },
      } as never);
      await openOrder();
      expect(
        await screen.findByText(/Priced at round 42: 2.5/),
      ).toBeInTheDocument();

      fireEvent.click(screen.getByRole("button", { name: "Settle order" }));

      expect(
        await screen.findByText(/Settlement confirmed/),
      ).toBeInTheDocument();
      expect(sendSettlePurchase).toHaveBeenCalledWith(
        31337,
        CONNECTED,
        VAULT,
        5n,
      );
      await waitFor(() =>
        expect(api.listPurchaseOrders).toHaveBeenCalledTimes(2),
      );
    });

    it("is refused while the project is paused", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD_PROJECT,
        paused: true,
      } as never);
      await openOrder();
      await screen.findByText(/Priced at round 42/);
      const button = screen.getByRole("button", { name: "Settle order" });
      expect(button).toBeDisabled();
      expect(button).toHaveAttribute(
        "title",
        expect.stringMatching(/refused while the project is paused/),
      );
    });

    it("waits while no round has landed", async () => {
      vi.mocked(readOrderPricing).mockResolvedValue({ state: "waiting" });
      await openOrder();
      await screen.findByText(/Waiting for a price round/);
      expect(
        screen.getByRole("button", { name: "Settle order" }),
      ).toBeDisabled();
      expect(sendSettlePurchase).not.toHaveBeenCalled();
    });

    it("shows the failure and no success when the settlement reverts", async () => {
      vi.mocked(sendSettlePurchase).mockRejectedValue(
        new Error("Transaction simulation failed: InvalidPrice."),
      );
      await openOrder();
      await screen.findByText(/Priced at round 42/);
      fireEvent.click(screen.getByRole("button", { name: "Settle order" }));
      expect(
        await screen.findByText(/Transaction simulation failed: InvalidPrice/),
      ).toBeInTheDocument();
      expect(screen.queryByText(/Settlement confirmed/)).toBeNull();
    });

    it("offers no actions on a closed order and shows its round, quote and refund", async () => {
      await openOrder({
        ...ORDER,
        status: "Settled" as const,
        pricedRound: "42",
        quoteAmount: "2500000000000000000",
        refund: "1500000000000000000",
      } as never);
      expect(screen.queryByRole("button", { name: "Settle order" })).toBeNull();
      expect(screen.queryByRole("button", { name: "Reject order" })).toBeNull();
      const detail = screen
        .getByRole("heading", { name: "Order 5" })
        .closest("section") as HTMLElement;
      expect(
        within(detail).getByText("Priced at round").nextSibling,
      ).toHaveTextContent("42");
      expect(
        within(detail).getByText("Quote at that round").nextSibling,
      ).toHaveTextContent("2.5");
      expect(
        within(detail).getByText("Refunded to buyer").nextSibling,
      ).toHaveTextContent("1.5");
    });
  });

  describe("order pricing", () => {
    it("prices a Pending order on its own strategy from its base round and creation time", async () => {
      await openOrder();
      expect(
        await screen.findByText(/Priced at round 42: 2.5/),
      ).toBeInTheDocument();
      expect(readOrderPricing).toHaveBeenCalledWith(
        31337,
        STRATEGY,
        3_000_000n,
        40n,
        1_800_000_000n,
      );
      expect(readOrderMinQuote).toHaveBeenCalledWith(31337, VAULT, 5n);
    });

    it("shows the minimum recorded with the order", async () => {
      await openOrder();
      const detail = screen
        .getByRole("heading", { name: "Order 5" })
        .closest("section") as HTMLElement;
      expect(
        (await within(detail).findByText("Minimum quote (fixed when ordered)"))
          .nextSibling,
      ).toHaveTextContent("1");
    });

    it("says a waiting order has no round yet", async () => {
      vi.mocked(readOrderPricing).mockResolvedValue({ state: "waiting" });
      await openOrder();
      expect(
        await screen.findByText(/Waiting for a price round/),
      ).toBeInTheDocument();
    });

    it("lets a refresh pick up a round that landed after the panel opened, and then settle", async () => {
      vi.mocked(readOrderPricing).mockResolvedValue({ state: "waiting" });
      await openOrder();
      await screen.findByText(/Waiting for a price round/);
      expect(
        screen.getByRole("button", { name: "Settle order" }),
      ).toBeDisabled();

      vi.mocked(readOrderPricing).mockResolvedValue(priced);
      fireEvent.click(screen.getByRole("button", { name: "Refresh price" }));

      expect(
        await screen.findByText(/Priced at round 42: 2.5/),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Settle order" }),
      ).toBeEnabled();
      fireEvent.click(screen.getByRole("button", { name: "Settle order" }));
      expect(
        await screen.findByText(/Settlement confirmed/),
      ).toBeInTheDocument();
      expect(sendSettlePurchase).toHaveBeenCalledTimes(1);
    });

    it("re-reads the project when the price is refreshed", async () => {
      await openOrder();
      await screen.findByText(/Priced at round 42/);
      expect(api.getProject).toHaveBeenCalledTimes(1);
      fireEvent.click(screen.getByRole("button", { name: "Refresh price" }));
      await waitFor(() => expect(api.getProject).toHaveBeenCalledTimes(2));
    });

    it("warns that settling refunds the deposit when the round prices above it", async () => {
      vi.mocked(readOrderPricing).mockResolvedValue({
        state: "priced",
        quoteAmount: 5_000_000_000_000_000_000n,
        roundId: 42n,
      });
      await openOrder();
      expect(
        await screen.findByText(
          /Priced at round 42: 5, above the buyer's deposit of 4\. Settling will refund the whole deposit/,
        ),
      ).toBeInTheDocument();
      expect(screen.queryByText(/Anyone can settle it/)).toBeNull();
    });

    it("warns that settling refunds the deposit when the round prices below the order's minimum", async () => {
      vi.mocked(readOrderPricing).mockResolvedValue({
        state: "priced",
        quoteAmount: 500_000_000_000_000_000n,
        roundId: 42n,
      });
      await openOrder();
      expect(
        await screen.findByText(
          /Priced at round 42: 0\.5, below this order's minimum of 1\. Settling will refund the whole deposit/,
        ),
      ).toBeInTheDocument();
    });

    it("compares against the minimum recorded with the order, not a live Vault minimum", async () => {
      vi.mocked(readOrderMinQuote).mockResolvedValue(0n);
      vi.mocked(readOrderPricing).mockResolvedValue({
        state: "priced",
        quoteAmount: 500_000_000_000_000_000n,
        roundId: 42n,
      });
      await openOrder();
      expect(
        await screen.findByText(
          /Priced at round 42: 0\.5\. Anyone can settle it/,
        ),
      ).toBeInTheDocument();
    });

    it("still lets the operator settle when the price could not be read", async () => {
      vi.mocked(readOrderPricing).mockRejectedValue(
        new Error("No injected wallet found."),
      );
      await openOrder();
      expect(
        await screen.findByText(/Could not read the price: No injected wallet/),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Settle order" }),
      ).toBeEnabled();
    });
  });

  describe("indexed pause state", () => {
    const stale = (paused: boolean) =>
      ({ ...FORWARD_PROJECT, paused, securityStale: true }) as never;

    it("does not block settlement on a paused flag the projection marks stale", async () => {
      vi.mocked(api.getProject).mockResolvedValue(stale(true));
      await openOrder();
      await screen.findByText(/Priced at round 42/);
      expect(
        screen.getByRole("button", { name: "Settle order" }),
      ).toBeEnabled();
      expect(screen.getByText(/may be out of date/)).toBeInTheDocument();

      fireEvent.click(screen.getByRole("button", { name: "Settle order" }));
      expect(
        await screen.findByText(/Settlement confirmed/),
      ).toBeInTheDocument();
      expect(sendSettlePurchase).toHaveBeenCalledTimes(1);
    });

    it("still offers rejection when the projection says unpaused but is marked stale", async () => {
      vi.mocked(api.getProject).mockResolvedValue(stale(false));
      await openOrder();
      expect(
        await screen.findByRole("button", { name: "Reject order" }),
      ).toBeEnabled();
      expect(screen.getByText(/may be out of date/)).toBeInTheDocument();
      expect(
        screen.getByText(
          /accepts a rejection only while the project is paused/,
        ),
      ).toBeInTheDocument();
    });

    it("lets the chain refuse a rejection the stale flag allowed", async () => {
      vi.mocked(api.getProject).mockResolvedValue(stale(false));
      vi.mocked(sendRejectPurchase).mockRejectedValue(
        new Error(
          "Transaction simulation failed: A purchase order can be rejected only while the project is paused. Nothing was sent.",
        ),
      );
      await openOrder();
      fireEvent.click(
        await screen.findByRole("button", { name: "Reject order" }),
      );
      expect(
        await screen.findByText(
          /rejected only while the project is paused\. Nothing was sent/,
        ),
      ).toBeInTheDocument();
    });

    it("keeps trusting a current projection: paused blocks settlement, unpaused hides rejection", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD_PROJECT,
        paused: true,
        securityStale: false,
      } as never);
      await openOrder();
      await screen.findByText(/Priced at round 42/);
      expect(
        screen.getByRole("button", { name: "Settle order" }),
      ).toBeDisabled();
      expect(screen.queryByText(/may be out of date/)).toBeNull();
    });

    it("re-reads the project after an action so the pause state can follow", async () => {
      vi.mocked(api.getProject)
        .mockResolvedValueOnce(FORWARD_PROJECT as never)
        .mockResolvedValue({ ...FORWARD_PROJECT, paused: true } as never);
      await openOrder();
      await screen.findByText(/Priced at round 42/);
      expect(screen.queryByRole("button", { name: "Reject order" })).toBeNull();

      fireEvent.click(screen.getByRole("button", { name: "Settle order" }));
      await screen.findByText(/Settlement confirmed/);

      expect(api.getProject).toHaveBeenCalledTimes(2);
      expect(
        await screen.findByRole("button", { name: "Reject order" }),
      ).toBeInTheDocument();
      // The page and the open panel stay put while the project reloads
      expect(
        screen.getByRole("heading", { name: "Order 5" }),
      ).toBeInTheDocument();
    });
  });

  describe("switching orders", () => {
    const SECOND = { ...ORDER, id: "6" };

    async function openFirstThenSecond() {
      vi.mocked(api.listPurchaseOrders).mockResolvedValue({
        items: [ORDER, SECOND],
      });
      vi.mocked(api.getPurchaseOrder).mockImplementation(async (id: string) =>
        id === "6" ? SECOND : ORDER,
      );
      renderWithWallet(<InventorySales />, { connected: true });
      const manage = await screen.findAllByRole("button", { name: "Manage" });
      fireEvent.click(manage[0]);
      await screen.findByRole("heading", { name: "Order 5" });
      await screen.findByText(/Priced at round 42/);
      return manage;
    }

    it("does not carry an in-flight settlement over to the next order", async () => {
      vi.mocked(sendSettlePurchase).mockReturnValue(new Promise(() => {}));
      const manage = await openFirstThenSecond();
      fireEvent.click(screen.getByRole("button", { name: "Settle order" }));
      await screen.findByText("Settling order…");

      fireEvent.click(manage[1]);
      await screen.findByRole("heading", { name: "Order 6" });
      await screen.findByText(/Priced at round 42/);

      expect(screen.queryByText("Settling order…")).toBeNull();
      expect(
        screen.getByRole("button", { name: "Settle order" }),
      ).toBeEnabled();
    });

    it("does not show one order's failure or success under another", async () => {
      vi.mocked(sendSettlePurchase).mockRejectedValueOnce(
        new Error("Transaction simulation failed: InvalidPrice."),
      );
      const manage = await openFirstThenSecond();
      fireEvent.click(screen.getByRole("button", { name: "Settle order" }));
      await screen.findByText(/InvalidPrice/);

      fireEvent.click(manage[1]);
      await screen.findByRole("heading", { name: "Order 6" });
      expect(screen.queryByText(/InvalidPrice/)).toBeNull();

      fireEvent.click(screen.getByRole("button", { name: "Settle order" }));
      await screen.findByText(/Settlement confirmed/);
      fireEvent.click(manage[0]);
      await screen.findByRole("heading", { name: "Order 5" });
      expect(screen.queryByText(/Settlement confirmed/)).toBeNull();
    });
  });

  describe("reject", () => {
    it("is not offered while the project is unpaused, and the note says why", async () => {
      await openOrder();
      expect(screen.queryByRole("button", { name: "Reject order" })).toBeNull();
      expect(
        screen.getByText(/rejected only while the project is paused/),
      ).toBeInTheDocument();
    });

    it("is offered while paused to a treasurer, with a note that the whole deposit returns", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD_PROJECT,
        paused: true,
      } as never);
      await openOrder();
      expect(
        await screen.findByRole("button", { name: "Reject order" }),
      ).toBeEnabled();
      expect(
        screen.getByText(
          /returns the buyer's whole deposit of 4 and closes the order as Rejected/,
        ),
      ).toBeInTheDocument();
    });

    it("sends rejectPurchase for the order from the treasurer's wallet", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD_PROJECT,
        paused: true,
      } as never);
      await openOrder();
      fireEvent.click(
        await screen.findByRole("button", { name: "Reject order" }),
      );

      expect(await screen.findByText(/Order rejected/)).toBeInTheDocument();
      expect(sendRejectPurchase).toHaveBeenCalledWith(
        31337,
        CONNECTED,
        VAULT,
        5n,
      );
      expect(sendSettlePurchase).not.toHaveBeenCalled();
    });

    it("is hidden from a wallet without TREASURER_ROLE on the Vault, even while paused", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD_PROJECT,
        paused: true,
        roles: {
          TREASURER_ROLE: ["0x9999999999999999999999999999999999999999"],
        },
      } as never);
      await openOrder();
      expect(
        await screen.findByText(/reject purchase orders/),
      ).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Reject order" })).toBeNull();
      expect(sendRejectPurchase).not.toHaveBeenCalled();
    });

    it("is hidden from a wallet that holds TREASURER_ROLE only on the escrow", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD_PROJECT,
        paused: true,
        rolesByContract: {
          vault: {
            TREASURER_ROLE: ["0x9999999999999999999999999999999999999999"],
          },
          redemptionEscrow: { TREASURER_ROLE: [CONNECTED] },
        },
      } as never);
      await openOrder();
      expect(
        await screen.findByText(/reject purchase orders/),
      ).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Reject order" })).toBeNull();
      expect(sendRejectPurchase).not.toHaveBeenCalled();
    });

    it("exports a rejectPurchase batch for a Safe holding the role, and broadcasts nothing", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD_PROJECT,
        paused: true,
        roles: { TREASURER_ROLE: [SAFE] },
      } as never);
      await openOrder();
      // The withdrawal card has its own Safe picker, so scope to the order card
      const card = screen
        .getByRole("heading", { name: "Order 5" })
        .closest("section") as HTMLElement;
      fireEvent.change(await within(card).findByLabelText(/Save a Safe/), {
        target: { value: SAFE },
      });
      fireEvent.click(
        await within(card).findByRole("button", {
          name: "Save rejection batch",
        }),
      );

      expect(await screen.findByText("Saved batch.")).toBeInTheDocument();
      const [chainId, safe, name, calls] =
        vi.mocked(exportSafeBatch).mock.calls[0];
      expect([chainId, safe, name]).toEqual([
        31337,
        SAFE,
        "Reject purchase order",
      ]);
      expect(calls).toHaveLength(1);
      expect(calls[0].to.toLowerCase()).toBe(VAULT);
      expect(
        decodeFunctionData({ abi: vaultAbi, data: calls[0].data }),
      ).toEqual({
        functionName: "rejectPurchase",
        args: [5n],
      });
      expect(sendRejectPurchase).not.toHaveBeenCalled();
    });

    it("shows the failure and no success when the rejection reverts", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD_PROJECT,
        paused: true,
      } as never);
      vi.mocked(sendRejectPurchase).mockRejectedValue(
        new Error("Transaction simulation failed: BuyerNotAllowed."),
      );
      await openOrder();
      fireEvent.click(
        await screen.findByRole("button", { name: "Reject order" }),
      );
      expect(await screen.findByText(/BuyerNotAllowed/)).toBeInTheDocument();
      expect(screen.queryByText(/Order rejected/)).toBeNull();
    });
  });
});
