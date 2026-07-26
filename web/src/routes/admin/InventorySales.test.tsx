import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { InventorySales } from "./InventorySales";
import { api } from "../../lib/client";
import { readPreviewBuy, sendWithdrawProceeds } from "../../lib/wallet";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";

// The Treasury withdrawal form is gated on the connected wallet holding
// TREASURER_ROLE (a UX gate — the on-chain onlyRole check is the real
// authorization). The fake wallet connects as this account, and the project
// grants it the role, so the form renders and can broadcast directly.
const CONNECTED = "0x1111111111111111111111111111111111111111";

// The admin sales forms must scale each amount by the RIGHT token's decimals:
// a buy preview is an RWA-token amount (Project.decimals,
// 6 here), but a treasury withdrawal pays out the QUOTE token, whose decimals
// (18 here) differ. Using the wrong one silently mis-scales money — these
// tests pin each field to its own decimals.
vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: vi.fn(),
      getInventory: vi.fn(),
      listPurchases: vi.fn(),
    },
  };
});

// Quote-token decimals are read on-chain (return 18 so a correct withdrawal
// conversion is distinguishable from the RWA token's 6 decimals); the purchase
// quote is a Vault.previewBuy read, and the withdraw broadcast + receipt wait
// are stubbed so the tests assert the exact calls without a chain.
vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Decimals: vi.fn().mockResolvedValue(18),
    readPreviewBuy: vi.fn().mockResolvedValue(2000000n),
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

    // 1 whole RWA unit at 6 decimals -> 1000000n, quoted by Vault.previewBuy
    // on the project's chain — no server round-trip.
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

    // 1 whole quote-token unit at 18 decimals -> 1000000000000000000n, NOT the
    // RWA token's 1000000n; sent to the vault from the connected wallet.
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

    // Once the quote decimals resolve (18), 10 > 5 disables the button and shows
    // the inline error — the withdrawal is never broadcast.
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Withdraw" })).toBeDisabled(),
    );
    expect(
      screen.getByText(/exceeds the vault's available quote balance/i),
    ).toBeInTheDocument();
    expect(sendWithdrawProceeds).not.toHaveBeenCalled();
  });
});
