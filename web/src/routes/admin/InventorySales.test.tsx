import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { InventorySales } from "./InventorySales";
import { api } from "../../lib/client";
import { readPreviewBuy, sendWithdrawProceeds } from "../../lib/wallet";
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
    },
  };
});

// Stub 18-decimal quote data versus 6-decimal RWA, previews and writes to check conversions without
// a chain
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
