import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { encodeErrorResult, parseAbi } from "viem";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";
import { Investor } from "./Investor";
import { api } from "../../lib/client";
import type { components } from "../../lib/api-types";
import {
  readErc20Allowance,
  readErc20Decimals,
  readForwardPriced,
  readOrderMinQuote,
  readOrderRound,
  readPreviewBuy,
  readPreviewRedeem,
  sendBuy,
  sendCancelPurchase,
  sendErc20Approve,
  sendRequestPurchase,
  sendRequestRedemption,
  sendSettlePurchase,
  waitForTxReceipt,
} from "../../lib/wallet";

vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: vi.fn(),
      getInventory: vi.fn(),
      getMyWalletStatus: vi.fn(),
      listRedemptions: vi.fn(),
      listTransactions: vi.fn(),
      listIncomeClaims: vi.fn(),
      listPurchaseOrders: vi.fn(),
      isAddressAllowed: vi.fn(),
    },
  };
});

vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Decimals: vi.fn().mockResolvedValue(6),
    readErc20Balance: vi.fn().mockResolvedValue("100000000"),
    readErc20Allowance: vi.fn().mockResolvedValue(10n ** 30n),
    readPreviewBuy: vi.fn().mockResolvedValue(2000000n),
    readPreviewRedeem: vi.fn().mockResolvedValue(5000000n),
    readOrderRound: vi.fn(),
    readForwardPriced: vi.fn(),
    readOrderMinQuote: vi.fn(),
    sendBuy: vi.fn().mockResolvedValue("0xbuy"),
    sendRequestPurchase: vi.fn().mockResolvedValue("0xorder"),
    sendSettlePurchase: vi.fn().mockResolvedValue("0xsettle"),
    sendCancelPurchase: vi.fn().mockResolvedValue("0xcancel"),
    sendRequestRedemption: vi.fn().mockResolvedValue("0xrequest"),
    sendErc20Approve: vi.fn().mockResolvedValue("0xapprove"),
    waitForTxReceipt: vi.fn().mockResolvedValue({ status: "success" }),
  };
});

type PurchaseOrder = components["schemas"]["PurchaseOrder"];
type Redemption = components["schemas"]["Redemption"];

const CONNECTED = "0x1111111111111111111111111111111111111111";
const STRATEGY = "0x0000000000000000000000000000000000000009";
const VAULT = "0x0000000000000000000000000000000000000003";
const ESCROW = "0x0000000000000000000000000000000000000004";
const PROJECT = {
  chainId: 31337,
  decimals: 6,
  quoteDecimals: 6,
  tokenUnit: "RWA",
  addresses: {
    token: "0x0000000000000000000000000000000000000001",
    quoteToken: "0x0000000000000000000000000000000000000002",
    vault: VAULT,
    redemptionEscrow: ESCROW,
  },
};
const FORWARD = {
  ...PROJECT,
  strategyKind: "Oracle",
  forwardPriced: true,
  oracle: { available: true, dealingCutoff: 7200 },
};

const now = () => Math.floor(Date.now() / 1000);

function order(over: Partial<PurchaseOrder> = {}): PurchaseOrder {
  return {
    id: "7",
    buyer: CONNECTED,
    recipient: CONNECTED,
    tokenAmount: "1000000",
    maxQuoteAmount: "2010000",
    strategy: STRATEGY,
    baseRound: "100",
    status: "Pending",
    createdAt: now() - 7200,
    // Cancel is open by default
    timeoutAt: now() - 3600,
    requestTxHash: "0xreq",
    ...over,
  };
}

function redemption(over: Partial<Redemption> = {}): Redemption {
  return {
    id: "11",
    beneficiary: CONNECTED,
    rwaAmount: "1000000",
    quoteAmount: "5000000",
    status: "Pending",
    createdAt: now() - 7200,
    timeoutAt: now() + 3600,
    beneficiaryAllowed: true,
    confirmations: 50,
    claimable: false,
    ...over,
  };
}

const ordersSection = () =>
  screen.getByRole("region", { name: "Purchase orders" });

function previewBuy(value = "1") {
  fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
    target: { value },
  });
  fireEvent.click(screen.getAllByRole("button", { name: "Preview quote" })[0]);
}

async function approveAndClick(button: string) {
  fireEvent.click(
    await screen.findByRole("button", { name: "1. Approve quote token spend" }),
  );
  await screen.findByText(/Approval submitted:/);
  const action = screen.getByRole("button", { name: button });
  await waitFor(() => expect(action).toBeEnabled());
  fireEvent.click(action);
}

let wallet: ReturnType<typeof installFakeWallet>;

beforeEach(() => {
  wallet = installFakeWallet({ account: CONNECTED, chainId: 31337 });
  vi.mocked(api.getProject)
    .mockReset()
    .mockResolvedValue(FORWARD as never);
  vi.mocked(api.getInventory)
    .mockReset()
    .mockResolvedValue({} as never);
  vi.mocked(api.listRedemptions).mockReset().mockResolvedValue({ items: [] });
  vi.mocked(api.listTransactions).mockReset().mockResolvedValue({ items: [] });
  vi.mocked(api.listIncomeClaims).mockReset().mockResolvedValue([]);
  vi.mocked(api.listPurchaseOrders)
    .mockReset()
    .mockResolvedValue({ items: [] });
  vi.mocked(api.isAddressAllowed)
    .mockReset()
    .mockResolvedValue({ allowed: true });
  vi.mocked(readOrderRound).mockReset().mockResolvedValue({ state: "waiting" });
  // The order recorded no minimum unless a test says otherwise
  vi.mocked(readOrderMinQuote).mockReset().mockResolvedValue(0n);
  // The chain says forward-priced unless a test overrides it
  vi.mocked(readForwardPriced).mockReset().mockResolvedValue(true);
  vi.mocked(readErc20Decimals).mockReset().mockResolvedValue(6);
  vi.mocked(readPreviewBuy).mockReset().mockResolvedValue(2000000n);
  vi.mocked(readPreviewRedeem).mockReset().mockResolvedValue(5000000n);
  vi.mocked(readErc20Allowance)
    .mockReset()
    .mockResolvedValue(10n ** 30n);
  vi.mocked(sendBuy).mockReset().mockResolvedValue("0xbuy");
  vi.mocked(sendRequestPurchase).mockReset().mockResolvedValue("0xorder");
  vi.mocked(sendSettlePurchase).mockReset().mockResolvedValue("0xsettle");
  vi.mocked(sendCancelPurchase).mockReset().mockResolvedValue("0xcancel");
  vi.mocked(sendRequestRedemption).mockReset().mockResolvedValue("0xrequest");
  vi.mocked(sendErc20Approve).mockReset().mockResolvedValue("0xapprove");
  vi.mocked(waitForTxReceipt)
    .mockReset()
    .mockResolvedValue({ status: "success" } as never);
  window.localStorage.clear();
});

afterEach(() => {
  wallet.uninstall();
});

describe("Investor buy on a forward-priced project", () => {
  it("places an order whose deposit is the slippage-ceiled estimate, instead of buying", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    previewBuy();
    await approveAndClick("2. Place order");

    await waitFor(() => expect(sendRequestPurchase).toHaveBeenCalled());
    const [chainId, from, to, args] =
      vi.mocked(sendRequestPurchase).mock.calls[0];
    expect([chainId, from, to]).toEqual([31337, CONNECTED, VAULT]);
    expect(args).toMatchObject({
      tokenAmount: 1000000n,
      // 2000000 estimate + default 50 bps
      maxQuoteAmount: 2010000n,
      recipient: CONNECTED,
    });
    expect(args.deadline).toBeGreaterThan(BigInt(now()));
    // The approval covers the deposit
    expect(vi.mocked(sendErc20Approve).mock.calls[0][4]).toBe(2010000n);
    expect(sendBuy).not.toHaveBeenCalled();
    await screen.findByText(/Order submitted:/);
  });

  it("says before confirming how the order is priced and when the deposit is refunded", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    previewBuy();

    const preview = await screen.findByRole("region", {
      name: "Purchase preview",
    });
    const text = preview.textContent ?? "";
    expect(text).toMatch(/only an estimate/);
    expect(text).toMatch(
      /published at least 2 hours after you place the order/,
    );
    expect(text).toMatch(/unused part comes back when the order settles/);
    expect(text).toMatch(
      /refunded, and no tokens are delivered, if the price exceeds the deposit/,
    );
    expect(text).toMatch(/inventory runs out/);
    expect(text).toMatch(/recipient stops being eligible/);
    expect(
      within(preview).getByText("Estimated quote (not locked)"),
    ).toBeVisible();
    expect(
      within(preview).getByText("Deposit (max quote amount)"),
    ).toBeVisible();
  });

  it("falls back to naming the dealing cut-off when the project does not report its length", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...FORWARD,
      oracle: { available: true },
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    previewBuy();

    const preview = await screen.findByRole("region", {
      name: "Purchase preview",
    });
    expect(preview.textContent).toMatch(
      /published at least the dealing cut-off after you place the order/,
    );
  });

  it("tells a forward project's investors that orders are priced at a later round", async () => {
    renderWithWallet(<Investor />);
    expect(
      await screen.findByText(/Orders are priced at a later feed round/),
    ).toBeVisible();
  });

  it("names the deposit in whole quote units in the notice", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    previewBuy();
    const preview = await screen.findByRole("region", {
      name: "Purchase preview",
    });
    expect(preview.textContent).toMatch(/deposit of 2\.01 quote tokens/);
  });

  it.each([
    ["false", { forwardPriced: false }],
    ["absent", {}],
  ])(
    "keeps the instant buy flow when forwardPriced is %s",
    async (_name, extra) => {
      vi.mocked(readForwardPriced).mockResolvedValue(false);
      vi.mocked(api.getProject).mockResolvedValue({
        ...PROJECT,
        ...extra,
      } as never);
      renderWithWallet(<Investor />, { connected: true });
      await waitFor(() => expect(api.getProject).toHaveBeenCalled());

      previewBuy();
      const preview = await screen.findByRole("region", {
        name: "Purchase preview",
      });
      expect(
        within(preview).getByText("Max quote amount (slippage bound)"),
      ).toBeVisible();
      expect(preview.textContent).not.toMatch(/estimate/);
      expect(
        screen.queryByRole("button", { name: "2. Place order" }),
      ).toBeNull();
      await approveAndClick("2. Buy");

      await waitFor(() => expect(sendBuy).toHaveBeenCalled());
      expect(sendRequestPurchase).not.toHaveBeenCalled();
      await screen.findByText(/Buy submitted:/);
    },
  );

  it("refuses a deposit one unit under the minimum before any approval", async () => {
    // Deposit is 2010000: a minimum of 2010001 is just out of reach
    vi.mocked(api.getProject).mockResolvedValue({
      ...FORWARD,
      minPurchaseQuote: "2010001",
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText(/Minimum purchase:/);

    previewBuy();
    expect(
      await screen.findByText(
        /deposit of 2\.01 quote tokens is below the project minimum/,
      ),
    ).toBeVisible();
    expect(
      screen.queryByRole("button", { name: "1. Approve quote token spend" }),
    ).toBeNull();
    expect(sendErc20Approve).not.toHaveBeenCalled();
  });

  it("accepts a deposit exactly at the minimum", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...FORWARD,
      minPurchaseQuote: "2010000",
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText(/Minimum purchase:/);

    previewBuy();
    expect(
      await screen.findByRole("button", {
        name: "1. Approve quote token spend",
      }),
    ).toBeEnabled();
  });

  it("measures the minimum against the deposit, not the estimate", async () => {
    // Estimate 2000000 is under the minimum, the 50 bps deposit of 2010000 meets it
    vi.mocked(api.getProject).mockResolvedValue({
      ...FORWARD,
      minPurchaseQuote: "2005000",
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText(/Minimum purchase:/);

    previewBuy();
    const preview = await screen.findByRole("region", {
      name: "Purchase preview",
    });
    expect(preview.textContent).toMatch(
      /estimate is below the project minimum, so if the price stays there the order is refunded/,
    );
  });

  it("pulls the preview back when lowering the slippage drops the deposit under the minimum", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...FORWARD,
      minPurchaseQuote: "2010000",
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText(/Minimum purchase:/);
    previewBuy();
    await screen.findByRole("button", { name: "1. Approve quote token spend" });

    fireEvent.change(
      screen.getByLabelText("Slippage (bps)", { selector: "#buy-slippage" }),
      {
        target: { value: "0" },
      },
    );

    expect(
      await screen.findByText(/is below the project minimum/),
    ).toBeVisible();
    expect(
      screen.queryByRole("button", { name: "1. Approve quote token spend" }),
    ).toBeNull();
  });

  it("still blocks the form when the oracle is unavailable, since there is no estimate to size a deposit", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...FORWARD,
      oracle: { available: false, error: "StalePrice", dealingCutoff: 7200 },
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText(
      /Prices unavailable: the price feed has not updated/,
    );

    fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
      target: { value: "1" },
    });
    expect(
      screen.getAllByRole("button", { name: "Preview quote" })[0],
    ).toBeDisabled();
  });

  it("tells the buyer to place an order when the strategy turned forward priced after the preview", async () => {
    vi.mocked(readForwardPriced).mockResolvedValue(false);
    vi.mocked(api.getProject).mockResolvedValue(PROJECT as never);
    vi.mocked(sendBuy).mockRejectedValue({
      data: encodeErrorResult({
        abi: parseAbi(["error ForwardPriced()"]),
        errorName: "ForwardPriced",
      }),
    });
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    previewBuy();
    await approveAndClick("2. Buy");

    expect(await screen.findByText(/place an order instead/)).toBeVisible();
  });
});

describe("Investor purchase orders", () => {
  it("shows no section on a project without forward pricing when the server has no orders endpoint", async () => {
    vi.mocked(api.getProject).mockResolvedValue(PROJECT as never);
    vi.mocked(api.listPurchaseOrders).mockRejectedValue(new Error("404"));
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    await screen.findByRole("heading", { name: "Redemption" });

    await waitFor(() => expect(api.listPurchaseOrders).toHaveBeenCalled());
    expect(
      screen.queryByRole("heading", { name: "Purchase orders" }),
    ).toBeNull();
  });

  it("still lists open orders and their Cancel path when the server omits the forward-pricing flag", async () => {
    vi.mocked(api.getProject).mockResolvedValue(PROJECT as never);
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });

    const section = await screen.findByRole("region", {
      name: "Purchase orders",
    });
    expect(
      await within(section).findByRole("button", { name: /^Cancel order/ }),
    ).toBeVisible();
  });

  it("hides the section on an instant v4 project with no orders", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      forwardPriced: false,
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.listPurchaseOrders).toHaveBeenCalled());

    expect(
      screen.queryByRole("heading", { name: "Purchase orders" }),
    ).toBeNull();
  });

  it("keeps showing orders left open when a project switched to instant pricing", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      forwardPriced: false,
    } as never);
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });

    expect(
      await screen.findByRole("heading", { name: "Purchase orders" }),
    ).toBeVisible();
  });

  it("asks only for the connected wallet's orders", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() =>
      expect(api.listPurchaseOrders).toHaveBeenCalledWith(
        expect.objectContaining({ address: CONNECTED }),
      ),
    );
    expect(
      await screen.findByText(/No purchase orders for this wallet/),
    ).toBeVisible();
  });

  it("reads the price on the order's own strategy with the order's round and time", async () => {
    const o = order({ baseRound: "123456789012345678901" });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [o] });
    renderWithWallet(<Investor />, { connected: true });
    await within(
      await screen.findByRole("region", { name: "Purchase orders" }),
    ).findByText("Waiting for a price.");

    expect(readOrderRound).toHaveBeenCalledWith(31337, STRATEGY, {
      tokenAmount: 1000000n,
      baseRound: 123456789012345678901n,
      createdAt: BigInt(o.createdAt),
    });
  });

  it("offers neither Settle nor Cancel before the timeout while waiting for a price", async () => {
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({
      items: [order({ timeoutAt: now() + 3600 })],
    });
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("Waiting for a price.");

    const section = ordersSection();
    expect(
      within(section).queryByRole("button", { name: /^Settle order/ }),
    ).toBeNull();
    expect(
      within(section).queryByRole("button", { name: /^Cancel order/ }),
    ).toBeNull();
    expect(section.textContent).toMatch(/you can cancel from/);
  });

  it("offers Cancel, not Settle, once the timeout passed with no price, and cancels with the order id", async () => {
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("Waiting for a price.");

    const section = ordersSection();
    expect(
      within(section).queryByRole("button", { name: /^Settle order/ }),
    ).toBeNull();
    fireEvent.click(
      await within(section).findByRole("button", { name: /^Cancel order/ }),
    );

    await waitFor(() =>
      expect(sendCancelPurchase).toHaveBeenCalledWith(
        31337,
        CONNECTED,
        VAULT,
        7n,
      ),
    );
    expect(sendSettlePurchase).not.toHaveBeenCalled();
    await screen.findByText(/Cancel submitted:/);
  });

  it("offers Cancel after the timeout when the pricing round is unusable", async () => {
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "unusable",
      reason: "the price feed returned an invalid price",
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });

    expect(
      await screen.findByText(
        /cannot price this order: the price feed returned an invalid price/,
      ),
    ).toBeVisible();
    const section = ordersSection();
    expect(
      within(section).getByRole("button", { name: /^Cancel order/ }),
    ).toBeVisible();
    expect(
      within(section).queryByRole("button", { name: /^Settle order/ }),
    ).toBeNull();
  });

  it("shows the quote and refund of a priced order and settles it, with no Cancel even after the timeout", async () => {
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 1900000n,
      roundId: 5n,
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });

    const section = await screen.findByRole("region", {
      name: "Purchase orders",
    });
    expect(
      await within(section).findByText(
        "Priced at 1.9 quote tokens. Settling pays that and returns 0.11 of your deposit.",
      ),
    ).toBeVisible();
    expect(
      within(section).queryByRole("button", { name: /^Cancel order/ }),
    ).toBeNull();
    fireEvent.click(
      within(section).getByRole("button", { name: /^Settle order/ }),
    );

    await waitFor(() =>
      expect(sendSettlePurchase).toHaveBeenCalledWith(
        31337,
        CONNECTED,
        VAULT,
        7n,
      ),
    );
    expect(sendCancelPurchase).not.toHaveBeenCalled();
    await screen.findByText(/Settle submitted:/);
  });

  it("warns that the whole deposit is refunded when the price lands above it", async () => {
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 2100000n,
      roundId: 5n,
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });

    expect(
      await screen.findByText(
        /Priced at 2\.1 quote tokens, above your deposit\. Settling refunds the whole deposit \(2\.01\) and delivers no tokens\./,
      ),
    ).toBeVisible();
  });

  it("withholds Settle while the project is paused and says so", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...FORWARD,
      paused: true,
    } as never);
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 1900000n,
      roundId: 5n,
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });

    expect(await screen.findByText(/once it is unpaused/)).toBeVisible();
    expect(
      within(ordersSection()).queryByRole("button", { name: /^Settle order/ }),
    ).toBeNull();
  });

  it("explains instead of offering actions when the buyer is not Allowed", async () => {
    vi.mocked(api.isAddressAllowed).mockResolvedValue({ allowed: false });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });

    expect(
      await screen.findByText(
        /buyer wallet is not Allowed in the compliance registry/,
      ),
    ).toBeVisible();
    const section = ordersSection();
    expect(
      within(section).queryByRole("button", { name: /^Cancel order/ }),
    ).toBeNull();
    expect(
      within(section).queryByRole("button", { name: /^Settle order/ }),
    ).toBeNull();
    expect(api.isAddressAllowed).toHaveBeenCalledWith(
      CONNECTED,
      expect.anything(),
    );
  });

  it("asks a wallet to connect before showing orders", async () => {
    renderWithWallet(<Investor />);
    expect(
      await screen.findByText("Connect a wallet to view your purchase orders."),
    ).toBeVisible();
    expect(api.listPurchaseOrders).not.toHaveBeenCalled();
  });

  it("offers Cancel only to the buyer even when the server lists another wallet's order", async () => {
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({
      items: [order({ buyer: "0x2222222222222222222222222222222222222222" })],
    });
    renderWithWallet(<Investor />, { connected: true });

    expect(
      await screen.findByText("Only the buyer can cancel this order."),
    ).toBeVisible();
    expect(
      within(ordersSection()).queryByRole("button", { name: /^Cancel order/ }),
    ).toBeNull();
  });

  describe("the minimum that settlement applies", () => {
    const priced = () =>
      vi.mocked(readOrderRound).mockResolvedValue({
        state: "priced",
        quote: 1900000n,
        roundId: 5n,
      });

    it("warns that a price under the minimum recorded on the order refunds the whole deposit", async () => {
      vi.mocked(readOrderMinQuote).mockResolvedValue(1950000n);
      priced();
      vi.mocked(api.listPurchaseOrders).mockResolvedValue({
        items: [order()],
      });
      renderWithWallet(<Investor />, { connected: true });

      expect(
        await screen.findByText(
          /Priced at 1\.9 quote tokens, below the order's minimum of 1\.95\. Settling refunds the whole deposit \(2\.01\) and delivers no tokens\.$/,
        ),
      ).toBeVisible();
      expect(readOrderMinQuote).toHaveBeenCalledWith(31337, VAULT, 7n);
    });

    it("fills against the order's own minimum although the project's minimum was raised since", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD,
        minPurchaseQuote: "1950000",
      } as never);
      vi.mocked(readOrderMinQuote).mockResolvedValue(0n);
      priced();
      vi.mocked(api.listPurchaseOrders).mockResolvedValue({
        items: [order()],
      });
      renderWithWallet(<Investor />, { connected: true });

      expect(
        await screen.findByText(
          "Priced at 1.9 quote tokens. Settling pays that and returns 0.11 of your deposit.",
        ),
      ).toBeVisible();
    });

    it("falls back to the project minimum and says so when the order's cannot be read", async () => {
      vi.mocked(api.getProject).mockResolvedValue({
        ...FORWARD,
        minPurchaseQuote: "1950000",
      } as never);
      vi.mocked(readOrderMinQuote).mockRejectedValue(new Error("rpc down"));
      priced();
      vi.mocked(api.listPurchaseOrders).mockResolvedValue({
        items: [order()],
      });
      renderWithWallet(<Investor />, { connected: true });

      const text = await screen.findByText(
        /below the project minimum of 1\.95/,
      );
      expect(text.textContent).toMatch(
        /minimum recorded on the order could not be read, so the project's current minimum is used/,
      );
    });

    it("shows the price alone until the order's minimum has been read", async () => {
      vi.mocked(readOrderMinQuote).mockReturnValue(new Promise(() => {}));
      priced();
      vi.mocked(api.listPurchaseOrders).mockResolvedValue({
        items: [order()],
      });
      renderWithWallet(<Investor />, { connected: true });

      expect(
        await screen.findByText("Priced at 1.9 quote tokens."),
      ).toBeVisible();
    });

    it("does not read the minimum for an order that has no price yet", async () => {
      vi.mocked(api.listPurchaseOrders).mockResolvedValue({
        items: [order()],
      });
      renderWithWallet(<Investor />, { connected: true });
      await screen.findByText("Waiting for a price.");

      expect(readOrderMinQuote).not.toHaveBeenCalled();
    });
  });

  it("still shows the price of a priced order whose deposit is malformed", async () => {
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 1900000n,
      roundId: 5n,
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({
      items: [order({ maxQuoteAmount: "lots" })],
    });
    renderWithWallet(<Investor />, { connected: true });

    expect(
      await screen.findByText("Priced at 1.9 quote tokens."),
    ).toBeVisible();
  });

  it("shows paid and refunded amounts of a settled order and offers no action", async () => {
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({
      items: [
        order({
          status: "Settled",
          quoteAmount: "1900000",
          refund: "110000",
          pricedRound: "5",
        }),
      ],
    });
    renderWithWallet(<Investor />, { connected: true });

    expect(
      await screen.findByText("Paid 1.9 quote tokens, refunded 0.11."),
    ).toBeVisible();
    expect(readOrderRound).not.toHaveBeenCalled();
    expect(
      within(ordersSection()).queryByRole("button", {
        name: /Settle|Cancel order/,
      }),
    ).toBeNull();
  });

  it.each([
    ["Refunded", "Refunded 2.01 quote tokens; no tokens delivered."],
    ["Cancelled", "Refunded 2.01 quote tokens."],
    ["Rejected", "Refunded 2.01 quote tokens."],
  ])("shows the refund of a %s order", async (status, text) => {
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({
      items: [
        order({
          status: status as PurchaseOrder["status"],
          refund: "2010000",
        }),
      ],
    });
    renderWithWallet(<Investor />, { connected: true });

    expect(await screen.findByText(text)).toBeVisible();
    expect(
      within(ordersSection()).queryByRole("button", {
        name: /Settle|Cancel order/,
      }),
    ).toBeNull();
  });

  it("renders a malformed baseRound without crashing and offers nothing", async () => {
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({
      items: [order({ baseRound: "round-one" })],
    });
    renderWithWallet(<Investor />, { connected: true });

    expect(
      await screen.findByText(/price for this order could not be read/),
    ).toBeVisible();
    expect(readOrderRound).not.toHaveBeenCalled();
    expect(
      within(ordersSection()).queryByRole("button", {
        name: /Settle|Cancel order/,
      }),
    ).toBeNull();
    // The rest of the page still works
    expect(screen.getByRole("heading", { name: "Redemption" })).toBeVisible();
  });

  it("offers nothing when the round read fails without a revert", async () => {
    vi.mocked(readOrderRound).mockRejectedValue(new Error("network down"));
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });

    expect(
      await screen.findByText(/price for this order could not be read/),
    ).toBeVisible();
    expect(
      within(ordersSection()).queryByRole("button", {
        name: /Settle|Cancel order/,
      }),
    ).toBeNull();
  });

  it("offers no action on an order whose id is malformed", async () => {
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 1900000n,
      roundId: 5n,
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({
      items: [order({ id: "seven" })],
    });
    renderWithWallet(<Investor />, { connected: true });

    expect(
      await screen.findByText(/price for this order could not be read/),
    ).toBeVisible();
    expect(
      within(ordersSection()).queryByRole("button", {
        name: /Settle|Cancel order/,
      }),
    ).toBeNull();
    expect(sendSettlePurchase).not.toHaveBeenCalled();
  });

  it("shows the contract's reason when settling reverts because no price landed", async () => {
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 1900000n,
      roundId: 5n,
    });
    vi.mocked(sendSettlePurchase).mockRejectedValue({
      data: encodeErrorResult({
        abi: parseAbi(["error RoundNotAvailable(uint80 roundId)"]),
        errorName: "RoundNotAvailable",
        args: [101n],
      }),
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });
    fireEvent.click(
      await within(
        await screen.findByRole("region", { name: "Purchase orders" }),
      ).findByRole("button", { name: /^Settle order/ }),
    );

    expect(
      await screen.findByText(/No price has been published/),
    ).toBeVisible();
    expect(screen.queryByText(/Settle submitted:/)).toBeNull();
  });

  it("re-reads orders and prices when Refresh orders is pressed", async () => {
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("Waiting for a price.");

    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 1900000n,
      roundId: 5n,
    });
    fireEvent.click(screen.getByRole("button", { name: "Refresh orders" }));

    expect(
      await screen.findByRole("button", { name: /^Settle order/ }),
    ).toBeVisible();
    expect(api.listPurchaseOrders).toHaveBeenCalledTimes(2);
  });
});

describe("Investor redemption on a forward-priced project", () => {
  it("shows a Pending forward request as priced at funding with the holder's minimum, not as a zero payout", async () => {
    vi.mocked(api.listRedemptions).mockResolvedValue({
      items: [
        redemption({
          quoteAmount: "0",
          strategy: STRATEGY,
          baseRound: "100",
          minQuoteOut: "4975000",
        }),
      ],
    });
    renderWithWallet(<Investor />, { connected: true });

    const row = (await screen.findByText("11")).closest("tr")!;
    expect(
      screen.getByText(/A request is priced when the issuer funds it/),
    ).toBeVisible();
    const cells = within(row).getAllByRole("cell");
    // ID, status, RWA amount, quote, timeout, actions
    expect(cells[3].textContent).toMatch(
      /^Priced at funding\s*\(minimum 4\.975\)$/,
    );
    expect(cells[3].textContent).not.toMatch(/^0/);
  });

  it("shows a funded forward request's real quote", async () => {
    vi.mocked(api.listRedemptions).mockResolvedValue({
      items: [
        redemption({
          status: "Funded",
          quoteAmount: "5100000",
          strategy: STRATEGY,
          minQuoteOut: "4975000",
          pricedRound: "104",
          claimable: true,
        }),
      ],
    });
    renderWithWallet(<Investor />, { connected: true });

    const row = (await screen.findByText("11")).closest("tr")!;
    expect(within(row).getAllByRole("cell")[3].textContent).toBe("5.1");
    expect(within(row).getByRole("button", { name: /Claim/ })).toBeVisible();
  });

  it("keeps an instant Pending request's quote as an amount", async () => {
    vi.mocked(api.getProject).mockResolvedValue(PROJECT as never);
    vi.mocked(api.listRedemptions).mockResolvedValue({
      items: [redemption()],
    });
    renderWithWallet(<Investor />, { connected: true });

    const row = (await screen.findByText("11")).closest("tr")!;
    expect(within(row).getAllByRole("cell")[3].textContent).toBe("5");
  });

  it("lets the holder cancel a forward request at its timeout", async () => {
    vi.mocked(api.listRedemptions).mockResolvedValue({
      items: [
        redemption({
          quoteAmount: "0",
          strategy: STRATEGY,
          minQuoteOut: "4975000",
          timeoutAt: now() - 1,
        }),
      ],
    });
    renderWithWallet(<Investor />, { connected: true });

    const row = (await screen.findByText("11")).closest("tr")!;
    expect(
      within(row).getByRole("button", { name: /^Cancel \(timeout elapsed\)/ }),
    ).toBeVisible();
  });

  it("calls the preview an estimate and sets minQuoteOut from its slippage floor", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(
      screen.getByLabelText("RWA amount to redeem (whole units)"),
      { target: { value: "2.5" } },
    );
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[1],
    );
    const preview = await screen.findByRole("region", {
      name: "Redemption request preview",
    });
    expect(
      within(preview).getByText("Estimated quote (not locked)"),
    ).toBeVisible();
    expect(preview.textContent).toMatch(
      /only an estimate: this request is priced when the issuer funds it/,
    );
    expect(preview.textContent).toMatch(
      /published at least 2 hours after you request/,
    );
    expect(preview.textContent).toMatch(/Funding closes at the timeout/);

    fireEvent.click(
      await screen.findByRole("button", { name: "1. Approve RWA spend" }),
    );
    await screen.findByText(/Approval submitted:/);
    const request = screen.getByRole("button", {
      name: "2. Request redemption",
    });
    await waitFor(() => expect(request).toBeEnabled());
    fireEvent.click(request);

    await waitFor(() => expect(sendRequestRedemption).toHaveBeenCalled());
    expect(vi.mocked(sendRequestRedemption).mock.calls[0][3]).toMatchObject({
      rwaAmount: 2500000n,
      // 5000000 estimate - default 50 bps
      minQuoteOut: 4975000n,
    });
  });

  it("keeps the snapshotted-quote wording on an instant project", async () => {
    vi.mocked(api.getProject).mockResolvedValue(PROJECT as never);
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(
      screen.getByLabelText("RWA amount to redeem (whole units)"),
      { target: { value: "1" } },
    );
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[1],
    );
    const preview = await screen.findByRole("region", {
      name: "Redemption request preview",
    });
    expect(within(preview).getByText("Snapshotted quote")).toBeVisible();
    expect(preview.textContent).not.toMatch(/estimate/);
  });
});

// A promise the test settles by hand, to hold a transaction between submit and receipt
function deferred<T = unknown>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("Investor purchase mode comes from the chain", () => {
  it("places an order when the chain is forward priced although the server says instant", async () => {
    vi.mocked(readForwardPriced).mockResolvedValue(true);
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      forwardPriced: false,
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    previewBuy();
    await approveAndClick("2. Place order");

    await waitFor(() => expect(sendRequestPurchase).toHaveBeenCalled());
    expect(sendBuy).not.toHaveBeenCalled();
    expect(readForwardPriced).toHaveBeenCalledWith(31337, VAULT);
  });

  it("offers no purchase at all when the mode cannot be read, even if the server says instant", async () => {
    vi.mocked(readForwardPriced).mockRejectedValue(new Error("rpc down"));
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      forwardPriced: false,
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    previewBuy();

    expect(await screen.findByText("rpc down")).toBeVisible();
    expect(
      screen.queryByRole("region", { name: "Purchase preview" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "1. Approve quote token spend" }),
    ).toBeNull();
    expect(screen.queryByRole("button", { name: "2. Buy" })).toBeNull();
    expect(sendErc20Approve).not.toHaveBeenCalled();
  });
});

describe("Investor in-flight transactions", () => {
  it("keeps Place order disabled until the receipt, then rechecks the allowance and reloads the orders", async () => {
    const receipt = deferred();
    vi.mocked(waitForTxReceipt).mockImplementation((hash) =>
      hash === "0xorder" ? (receipt.promise as never) : ({} as never),
    );
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    previewBuy();
    await approveAndClick("2. Place order");
    await waitFor(() => expect(sendRequestPurchase).toHaveBeenCalledTimes(1));

    const place = screen.getByRole("button", { name: "2. Place order" });
    expect(place).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "1. Approve quote token spend" }),
    ).toBeDisabled();
    fireEvent.click(place);
    expect(sendRequestPurchase).toHaveBeenCalledTimes(1);

    // The deposit is pulled when the order is mined
    vi.mocked(readErc20Allowance).mockResolvedValue(0n);
    const listCalls = vi.mocked(api.listPurchaseOrders).mock.calls.length;
    receipt.resolve({ status: "success" });

    await waitFor(() =>
      expect(
        vi.mocked(api.listPurchaseOrders).mock.calls.length,
      ).toBeGreaterThan(listCalls),
    );
    // The approval is used up, so a second order needs a new approval
    await waitFor(() =>
      expect(
        screen.getByRole("button", {
          name: "1. Approve quote token spend",
        }),
      ).toBeEnabled(),
    );
    expect(
      screen.getByRole("button", { name: "2. Place order" }),
    ).toBeDisabled();
  });

  it("reports an approval that was mined but reverted instead of waiting silently", async () => {
    vi.mocked(waitForTxReceipt).mockRejectedValue(
      new Error("Transaction 0xapprove was mined but reverted on-chain."),
    );
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    previewBuy();
    fireEvent.click(
      await screen.findByRole("button", {
        name: "1. Approve quote token spend",
      }),
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "mined but reverted on-chain",
    );
    expect(
      screen.getByRole("button", { name: "2. Place order" }),
    ).toBeDisabled();
  });

  it("reports an order that was mined but reverted and lets the buyer retry", async () => {
    vi.mocked(waitForTxReceipt).mockImplementation((hash) =>
      hash === "0xorder"
        ? Promise.reject(
            new Error("Transaction 0xorder was mined but reverted on-chain."),
          )
        : Promise.resolve({} as never),
    );
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    previewBuy();
    await approveAndClick("2. Place order");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "mined but reverted on-chain",
    );
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "2. Place order" }),
      ).toBeEnabled(),
    );
  });

  it("disables every Settle and Cancel button from submit until the receipt, then reloads the list", async () => {
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 1900000n,
      roundId: 5n,
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({
      items: [order(), order({ id: "8" })],
    });
    const receipt = deferred();
    vi.mocked(waitForTxReceipt).mockReturnValue(receipt.promise as never);
    renderWithWallet(<Investor />, { connected: true });
    const section = await screen.findByRole("region", {
      name: "Purchase orders",
    });
    const settle7 = await within(section).findByRole("button", {
      name: "Settle order 7",
    });
    const settle8 = within(section).getByRole("button", {
      name: "Settle order 8",
    });

    fireEvent.click(settle7);
    await waitFor(() => expect(sendSettlePurchase).toHaveBeenCalledTimes(1));
    expect(settle7).toBeDisabled();
    expect(settle8).toBeDisabled();
    fireEvent.click(settle7);
    expect(sendSettlePurchase).toHaveBeenCalledTimes(1);

    const listCalls = vi.mocked(api.listPurchaseOrders).mock.calls.length;
    receipt.resolve({ status: "success" });
    await waitFor(() =>
      expect(
        vi.mocked(api.listPurchaseOrders).mock.calls.length,
      ).toBeGreaterThan(listCalls),
    );
    expect(
      await within(ordersSection()).findByRole("button", {
        name: "Settle order 7",
      }),
    ).toBeEnabled();
  });

  it("shows a reverted settlement as an error and frees the buttons", async () => {
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 1900000n,
      roundId: 5n,
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    vi.mocked(waitForTxReceipt).mockRejectedValue(
      new Error("Transaction 0xsettle was mined but reverted on-chain."),
    );
    renderWithWallet(<Investor />, { connected: true });
    fireEvent.click(
      await within(
        await screen.findByRole("region", { name: "Purchase orders" }),
      ).findByRole("button", { name: "Settle order 7" }),
    );

    expect(await within(ordersSection()).findByRole("alert")).toHaveTextContent(
      "mined but reverted on-chain",
    );
    expect(
      within(ordersSection()).getByRole("button", { name: "Settle order 7" }),
    ).toBeEnabled();
  });

  it("holds redemption Claim and Cancel buttons until the receipt, then reloads the requests", async () => {
    vi.mocked(api.listRedemptions).mockResolvedValue({
      items: [
        redemption({ claimable: true, status: "Funded" }),
        redemption({ id: "12", timeoutAt: now() - 1 }),
      ],
    });
    const receipt = deferred();
    vi.mocked(waitForTxReceipt).mockReturnValue(receipt.promise as never);
    renderWithWallet(<Investor />, { connected: true });
    const claim = await screen.findByRole("button", {
      name: "Claim (permissionless) redemption 11",
    });
    const cancel = screen.getByRole("button", {
      name: "Cancel (timeout elapsed) redemption 12",
    });

    fireEvent.click(claim);
    await waitFor(() => expect(waitForTxReceipt).toHaveBeenCalled());
    expect(claim).toBeDisabled();
    expect(cancel).toBeDisabled();

    const listCalls = vi.mocked(api.listRedemptions).mock.calls.length;
    receipt.resolve({ status: "success" });
    await waitFor(() =>
      expect(vi.mocked(api.listRedemptions).mock.calls.length).toBeGreaterThan(
        listCalls,
      ),
    );
  });
});

describe("Investor forward redemption estimate", () => {
  it("refuses a request whose estimate is zero before any approval", async () => {
    vi.mocked(readPreviewRedeem).mockResolvedValue(0n);
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(
      screen.getByLabelText("RWA amount to redeem (whole units)"),
      { target: { value: "0.000001" } },
    );
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[1],
    );

    expect(
      await screen.findByText(/The estimated quote is zero/),
    ).toBeVisible();
    expect(
      screen.queryByRole("region", { name: "Redemption request preview" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "2. Request redemption" }),
    ).toBeNull();
    expect(sendRequestRedemption).not.toHaveBeenCalled();
  });
});

describe("Investor quote amounts before decimals are known", () => {
  it("scales order amounts at once when the project carries its quote decimals", async () => {
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 1900000n,
      roundId: 5n,
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });

    const section = await screen.findByRole("region", {
      name: "Purchase orders",
    });
    // The first paint of the priced row is already scaled, never raw base units
    const price = await within(section).findByText(/^Priced at /);
    expect(price.textContent).toBe(
      "Priced at 1.9 quote tokens. Settling pays that and returns 0.11 of your deposit.",
    );
    expect(section.textContent).not.toMatch(/1,900,000|2,010,000/);
  });

  it("labels amounts as minimal units when quote decimals cannot be resolved", async () => {
    const { quoteDecimals: _omit, ...withoutDecimals } = FORWARD;
    void _omit;
    vi.mocked(api.getProject).mockResolvedValue(withoutDecimals as never);
    vi.mocked(readErc20Decimals).mockRejectedValue(new Error("no decimals"));
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 1900000n,
      roundId: 5n,
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({ items: [order()] });
    renderWithWallet(<Investor />, { connected: true });

    const section = await screen.findByRole("region", {
      name: "Purchase orders",
    });
    expect(
      await within(section).findByText(
        "Priced at 1,900,000 quote-token minimal units. Settling pays that and returns 110,000 of your deposit.",
      ),
    ).toBeVisible();
    expect(section.textContent).not.toMatch(/quote tokens/);
    expect(
      within(section).getByRole("columnheader", {
        name: "Deposit (minimal units)",
      }),
    ).toBeVisible();
  });
});

describe("Investor purchase order timeout", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("offers Cancel as soon as the timeout passes, with no other render", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({
      items: [order({ timeoutAt: now() + 5 })],
    });
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("Waiting for a price.");
    expect(
      within(ordersSection()).queryByRole("button", { name: /^Cancel order/ }),
    ).toBeNull();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000);
    });

    expect(
      await within(ordersSection()).findByRole("button", {
        name: "Cancel order 7",
      }),
    ).toBeVisible();
  });
});

describe("Investor order table accessibility", () => {
  it("names each button after its order and gives the action column a header", async () => {
    vi.mocked(readOrderRound).mockResolvedValue({
      state: "priced",
      quote: 1900000n,
      roundId: 5n,
    });
    vi.mocked(api.listPurchaseOrders).mockResolvedValue({
      items: [order(), order({ id: "8" })],
    });
    renderWithWallet(<Investor />, { connected: true });
    const section = await screen.findByRole("region", {
      name: "Purchase orders",
    });

    expect(
      await within(section).findByRole("button", { name: "Settle order 7" }),
    ).toBeVisible();
    expect(
      within(section).getByRole("button", { name: "Settle order 8" }),
    ).toBeVisible();
    expect(
      within(section).getByRole("columnheader", { name: "Actions" }),
    ).toBeInTheDocument();
  });

  it("names redemption buttons after their request", async () => {
    vi.mocked(api.listRedemptions).mockResolvedValue({
      items: [
        redemption({ claimable: true, status: "Funded" }),
        redemption({ id: "12", timeoutAt: now() - 1 }),
      ],
    });
    renderWithWallet(<Investor />, { connected: true });

    expect(
      await screen.findByRole("button", {
        name: "Claim (permissionless) redemption 11",
      }),
    ).toBeVisible();
    expect(
      screen.getByRole("button", {
        name: "Cancel (timeout elapsed) redemption 12",
      }),
    ).toBeVisible();
  });
});

describe("Investor redemption Cancel at the timeout", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("offers Cancel as soon as a Pending request's timeout passes, with no other render", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.mocked(api.listRedemptions).mockResolvedValue({
      items: [redemption({ timeoutAt: now() + 5 })],
    });
    renderWithWallet(<Investor />, { connected: true });
    const row = (await screen.findByText("11")).closest("tr")!;
    expect(
      within(row).queryByRole("button", {
        name: /^Cancel \(timeout elapsed\)/,
      }),
    ).toBeNull();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000);
    });

    expect(
      await within(row).findByRole("button", {
        name: "Cancel (timeout elapsed) redemption 11",
      }),
    ).toBeVisible();
  });
});
