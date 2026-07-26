import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Investor } from "./Investor";
import { api } from "../../lib/client";
import {
  readErc20Allowance,
  readPreviewBuy,
  readPreviewRedeem,
  sendBuy,
  sendCancelRedemption,
  sendClaimRedemption,
  sendErc20Approve,
  sendRequestRedemption,
  waitForTxReceipt,
} from "../../lib/wallet";

// The investor forms take human whole-unit amounts and must convert them to the
// token's minimal units before anything reaches a contract.
// A 6-decimal token is used throughout so "1" must become "1000000".
vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: vi.fn(),
      // The connected-wallet redemptions/history lists + claim/cancel flow.
      listRedemptions: vi.fn(),
      listTransactions: vi.fn(),
    },
  };
});

// resolveQuoteDecimals reads the quote token on-chain; stub the wallet read so
// the (display-only) quote-decimals lookup doesn't need an injected provider.
// The two price previews are on-chain views as well and are stubbed with the
// raw bigint the contracts would return. The buy/claim/cancel senders are
// spied so the flows can be asserted without a chain; the approve trio lets a
// test reach the Buy button, which stays disabled until a confirmed approval
// leaves a sufficient allowance.
vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Decimals: vi.fn().mockResolvedValue(6),
    readPreviewBuy: vi.fn().mockResolvedValue(2000000n),
    readPreviewRedeem: vi.fn().mockResolvedValue(5000000n),
    sendBuy: vi.fn().mockResolvedValue("0xhash"),
    sendRequestRedemption: vi.fn().mockResolvedValue("0xhash"),
    sendClaimRedemption: vi.fn().mockResolvedValue("0xhash"),
    sendCancelRedemption: vi.fn().mockResolvedValue("0xhash"),
    sendErc20Approve: vi.fn().mockResolvedValue("0xapprove"),
    waitForTxReceipt: vi.fn().mockResolvedValue(undefined),
    readErc20Allowance: vi.fn().mockResolvedValue(10n ** 30n),
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
    redemptionEscrow: "0x0000000000000000000000000000000000000004",
  },
};

describe("Investor amount conversion", () => {
  beforeEach(() => {
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    vi.mocked(readPreviewBuy).mockClear().mockResolvedValue(2000000n);
    vi.mocked(readPreviewRedeem).mockClear().mockResolvedValue(5000000n);
    window.localStorage.clear();
  });

  it("prices the buy token amount in minimal units for the token's decimals", async () => {
    renderWithWallet(<Investor />);
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
      target: { value: "1" },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[0],
    );

    // "1" whole unit at 6 decimals -> 1000000n minimal units, priced by the
    // project's own Vault on the project's chain.
    await waitFor(() =>
      expect(readPreviewBuy).toHaveBeenCalledWith(
        PROJECT.chainId,
        PROJECT.addresses.vault,
        1000000n,
      ),
    );
  });

  it("prices the redemption amount in minimal units, including fractions", async () => {
    renderWithWallet(<Investor />);
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(
      screen.getByLabelText("RWA amount to redeem (whole units)"),
      { target: { value: "2.5" } },
    );
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[1],
    );

    await waitFor(() =>
      expect(readPreviewRedeem).toHaveBeenCalledWith(
        PROJECT.chainId,
        PROJECT.addresses.redemptionEscrow,
        2500000n,
      ),
    );
  });

  it("rejects an over-precise amount with a friendly error and sends nothing", async () => {
    renderWithWallet(<Investor />);
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
      target: { value: "1.2345678" },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[0],
    );

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent('"1.2345678"'),
    );
    expect(readPreviewBuy).not.toHaveBeenCalled();
  });
});

const CONNECTED = "0x1111111111111111111111111111111111111111";

function makeRedemption(over: Record<string, unknown>) {
  const now = Math.floor(Date.now() / 1000);
  return {
    beneficiary: CONNECTED,
    rwaAmount: "1000000",
    quoteAmount: "2000000",
    createdAt: now,
    timeoutAt: now + 3600,
    beneficiaryAllowed: true,
    confirmations: 3,
    claimable: false,
    ...over,
  };
}

/** The <tr> containing a redemption row's id cell. */
function rowById(id: string): HTMLElement {
  return screen.getByText(id).closest("tr") as HTMLElement;
}

describe("Investor redemptions list (connected wallet)", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: CONNECTED, chainId: 31337 });
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    vi.mocked(api.listTransactions)
      .mockReset()
      .mockResolvedValue({ items: [] });
    vi.mocked(sendClaimRedemption).mockClear().mockResolvedValue("0xhash");
    vi.mocked(sendCancelRedemption).mockClear().mockResolvedValue("0xhash");
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("shows Claim on a claimable request and Cancel on a timed-out Pending one, not vice-versa", async () => {
    const now = Math.floor(Date.now() / 1000);
    vi.mocked(api.listRedemptions)
      .mockReset()
      .mockResolvedValue({
        items: [
          makeRedemption({ id: "10", status: "Funded", claimable: true }),
          makeRedemption({
            id: "11",
            status: "Pending",
            timeoutAt: now - 3600,
          }),
        ],
      });

    renderWithWallet(<Investor />, { connected: true });

    // The list is address-filtered to the connected wallet.
    await waitFor(() =>
      expect(api.listRedemptions).toHaveBeenCalledWith(
        expect.objectContaining({ address: CONNECTED }),
      ),
    );
    await screen.findByText("10");

    const claimable = rowById("10");
    expect(
      within(claimable).getByRole("button", { name: "Claim (permissionless)" }),
    ).toBeInTheDocument();
    expect(
      within(claimable).queryByRole("button", {
        name: "Cancel (timeout elapsed)",
      }),
    ).toBeNull();

    const timedOut = rowById("11");
    expect(
      within(timedOut).getByRole("button", {
        name: "Cancel (timeout elapsed)",
      }),
    ).toBeInTheDocument();
    expect(
      within(timedOut).queryByRole("button", {
        name: "Claim (permissionless)",
      }),
    ).toBeNull();
  });

  it("offers no Claim/Cancel on a Funded-but-not-yet-claimable request", async () => {
    vi.mocked(api.listRedemptions)
      .mockReset()
      .mockResolvedValue({
        items: [
          makeRedemption({ id: "12", status: "Funded", claimable: false }),
        ],
      });

    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("12");

    const row = rowById("12");
    expect(
      within(row).queryByRole("button", { name: "Claim (permissionless)" }),
    ).toBeNull();
    expect(
      within(row).queryByRole("button", { name: "Cancel (timeout elapsed)" }),
    ).toBeNull();
  });

  it("claims a claimable request by encoding claimRedemption(id) for the escrow", async () => {
    vi.mocked(api.listRedemptions)
      .mockReset()
      .mockResolvedValue({
        items: [
          makeRedemption({ id: "10", status: "Funded", claimable: true }),
        ],
      });

    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("10");

    fireEvent.click(
      within(rowById("10")).getByRole("button", {
        name: "Claim (permissionless)",
      }),
    );

    await waitFor(() =>
      expect(sendClaimRedemption).toHaveBeenCalledWith(
        PROJECT.chainId,
        CONNECTED,
        PROJECT.addresses.redemptionEscrow,
        10n,
      ),
    );
    await screen.findByText(/Claim submitted:/);
  });

  it("cancels a timed-out Pending request by encoding cancelRedemption(id) for the escrow", async () => {
    const now = Math.floor(Date.now() / 1000);
    vi.mocked(api.listRedemptions)
      .mockReset()
      .mockResolvedValue({
        items: [
          makeRedemption({
            id: "11",
            status: "Pending",
            timeoutAt: now - 3600,
          }),
        ],
      });

    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("11");

    fireEvent.click(
      within(rowById("11")).getByRole("button", {
        name: "Cancel (timeout elapsed)",
      }),
    );

    await waitFor(() =>
      expect(sendCancelRedemption).toHaveBeenCalledWith(
        PROJECT.chainId,
        CONNECTED,
        PROJECT.addresses.redemptionEscrow,
        11n,
      ),
    );
    await screen.findByText(/Cancel submitted:/);
  });
});

// Buy and redemption-request are encoded in the browser and broadcast from the
// connected wallet: the target is the project's own Vault/escrow, and the
// on-chain arguments must carry the amounts, the slippage bound, the connected
// recipient and a future deadline the page derived itself.
describe("Investor buy and redemption request (client-encoded)", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: CONNECTED, chainId: 31337 });
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    vi.mocked(readPreviewBuy).mockClear().mockResolvedValue(2000000n);
    vi.mocked(readPreviewRedeem).mockClear().mockResolvedValue(5000000n);
    vi.mocked(api.listRedemptions).mockReset().mockResolvedValue({ items: [] });
    vi.mocked(api.listTransactions)
      .mockReset()
      .mockResolvedValue({ items: [] });
    vi.mocked(sendBuy).mockClear().mockResolvedValue("0xhash");
    vi.mocked(sendRequestRedemption).mockClear().mockResolvedValue("0xhash");
    vi.mocked(sendErc20Approve).mockClear().mockResolvedValue("0xapprove");
    vi.mocked(waitForTxReceipt).mockClear().mockResolvedValue(undefined);
    vi.mocked(readErc20Allowance)
      .mockClear()
      .mockResolvedValue(10n ** 30n);
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("buys with the quoted amount, the slippage-ceiled max spend and the connected recipient", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
      target: { value: "1" },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[0],
    );

    fireEvent.click(
      await screen.findByRole("button", {
        name: "1. Approve quote token spend",
      }),
    );
    await screen.findByText(/Approval submitted:/);
    const buy = screen.getByRole("button", { name: "2. Buy" });
    await waitFor(() => expect(buy).toBeEnabled());
    fireEvent.click(buy);

    await waitFor(() => expect(sendBuy).toHaveBeenCalled());
    const [chainId, from, to, args] = vi.mocked(sendBuy).mock.calls[0];
    expect([chainId, from, to]).toEqual([
      PROJECT.chainId,
      CONNECTED,
      PROJECT.addresses.vault,
    ]);
    expect(args).toMatchObject({
      tokenAmount: 1000000n,
      // 2000000 quote + the default 50 bps.
      maxQuoteAmount: 2010000n,
      recipient: CONNECTED,
    });
    expect(args.deadline).toBeGreaterThan(
      BigInt(Math.floor(Date.now() / 1000)),
    );

    await screen.findByText(/Buy submitted:/);
  });

  it("requests a redemption with the minimal-unit amount and the slippage-floored min quote out", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(
      screen.getByLabelText("RWA amount to redeem (whole units)"),
      { target: { value: "2.5" } },
    );
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[1],
    );

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
    const [chainId, from, to, args] = vi.mocked(sendRequestRedemption).mock
      .calls[0];
    expect([chainId, from, to]).toEqual([
      PROJECT.chainId,
      CONNECTED,
      PROJECT.addresses.redemptionEscrow,
    ]);
    expect(args).toMatchObject({
      rwaAmount: 2500000n,
      // 5000000 quote less the default 50 bps.
      minQuoteOut: 4975000n,
    });
    expect(args.deadline).toBeGreaterThan(
      BigInt(Math.floor(Date.now() / 1000)),
    );

    await screen.findByText(/Request submitted:/);
  });
});
